package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
)

// batchDecisionOutputSchema is the JSON Schema a batch inference call answers
// with: an array of per-rule decisions keyed by the rule_id the prompt gives.
const batchDecisionOutputSchema = `{"type":"object","properties":{"decisions":{"type":"array","items":{"type":"object","properties":{"rule_id":{"type":"string"},"decision":{"type":"string","enum":["allow","block"]}},"required":["rule_id","decision"],"additionalProperties":false}}},"required":["decisions"],"additionalProperties":false}`

// batchRuleDecision is one rule's outcome inside a batch reply. A rule the model
// omitted, or a call that failed, is errored so the read site applies the entry's
// on_error policy.
type batchRuleDecision struct {
	block     bool
	errored   bool
	errorCode string
}

// batchGroupResult is the outcome of one batch call for one inference point. Every
// participating rule reads its own decision from decisions, and each records a
// layer carrying the shared input, timing, and call-level upstream metadata.
type batchGroupResult struct {
	model       string
	inputJSON   string
	upstream    UpstreamMetadata
	startedAt   time.Time
	completedAt time.Time
	decisions   map[string]batchRuleDecision
}

// batchInferenceMemo holds the batch results for one event, keyed by the full
// inference-point identity, so the eval matrix reads a rule's decision without
// issuing its own call. Keying by the whole point keeps two points that share an
// endpoint and model but differ in timeout, reasoning, or context policy in
// separate calls rather than silently merging them.
type batchInferenceMemo struct {
	groups map[config.InferencePoint]*batchGroupResult
}

type batchInferenceMemoKey struct{}

func withBatchInferenceMemo(ctx context.Context, memo *batchInferenceMemo) context.Context {
	if memo == nil {
		return ctx
	}
	return context.WithValue(ctx, batchInferenceMemoKey{}, memo)
}

func batchInferenceMemoFromContext(ctx context.Context) *batchInferenceMemo {
	memo, _ := ctx.Value(batchInferenceMemoKey{}).(*batchInferenceMemo)
	return memo
}

// verdictFor returns the recorded verdict for a rule at an inference point, plus
// whether the batch memo carries one. A false result tells the caller to fall back
// to an individual call.
func (memo *batchInferenceMemo) verdictFor(point config.InferencePoint, ruleName string) (*pointVerdict, bool) {
	if memo == nil {
		return nil, false
	}
	group, ok := memo.groups[point]
	if !ok {
		return nil, false
	}
	decision, ok := group.decisions[ruleName]
	if !ok {
		return nil, false
	}
	verdict := pointVerdict{
		block:       decision.block,
		errored:     decision.errored,
		inputJSON:   group.inputJSON,
		outputJSON:  batchRuleOutputJSON(decision),
		errorCode:   decision.errorCode,
		model:       group.model,
		upstream:    group.upstream,
		startedAt:   group.startedAt,
		completedAt: group.completedAt,
	}
	return &verdict, true
}

// batchParticipant names one rule judged in a batch call and the intent the model
// applies to the command for that rule.
type batchParticipant struct {
	ruleName string
	intent   string
}

// batchGroupPlan collects the rules that share one inference point for this event,
// so the planner issues a single call per point.
type batchGroupPlan struct {
	point        config.InferencePoint
	participants []batchParticipant
	seen         map[string]bool
}

// runBatchInference issues one inference call per inference point for every
// applicable rule whose infer entries opt into fanout=batch, and returns a memo
// the eval matrix reads. It fetches the conversation context once per distinct
// context configuration and attaches each group only the context fetched from its
// own point, so a point never receives conversation data fetched for a different
// endpoint. It returns nil when no rule opts in.
func runBatchInference(
	ctx context.Context,
	fields *FieldSet,
	rulesSlice []config.Rule,
	system string,
	eventName string,
	getenv func(string) string,
) *batchInferenceMemo {
	runtime := inferRuntimeFromContext(ctx)
	if runtime == nil {
		return nil
	}
	groups, order := collectBatchGroups(rulesSlice, system, eventName, getenv)
	if len(groups) == 0 {
		return nil
	}
	fetched := runtime.prefetchGroupContexts(ctx, order, fields)
	command := fields.CommandValue()
	memo := &batchInferenceMemo{groups: make(map[config.InferencePoint]*batchGroupResult, len(groups))}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, point := range order {
		plan := groups[point]
		wg.Add(1)
		go func(point config.InferencePoint, plan *batchGroupPlan) {
			defer wg.Done()
			defer func() {
				if recovered := recover(); recovered != nil {
					slog.ErrorContext(
						ctx,
						"batch inference goroutine panicked",
						"err", fmt.Errorf("panic: %v", recovered),
						"model", point.Model,
					)
				}
			}()
			contextJSON, contextErrored := groupContext(point, fields, fetched)
			result := runtime.evaluateBatchGroup(ctx, plan, command, contextJSON, contextErrored)
			mu.Lock()
			memo.groups[point] = result
			mu.Unlock()
		}(point, plan)
	}
	wg.Wait()
	return memo
}

// collectBatchGroups walks the applicable rules and groups every fanout=batch infer
// entry by its full inference point. It returns the groups and a stable point order.
func collectBatchGroups(
	rulesSlice []config.Rule,
	system string,
	eventName string,
	getenv func(string) string,
) (map[config.InferencePoint]*batchGroupPlan, []config.InferencePoint) {
	groups := map[config.InferencePoint]*batchGroupPlan{}
	var order []config.InferencePoint
	for i := range rulesSlice {
		rule := &rulesSlice[i]
		if !appliesToEvent(rule, system, eventName) {
			continue
		}
		if envGuardFires(getenv, rule.DisableIfEnv) {
			continue
		}
		for _, eval := range rule.Eval {
			if eval.Kind != config.EvalKindInfer || eval.Fanout != config.FanoutBatch {
				continue
			}
			point, ok := rule.EvalInference[eval.Use]
			if !ok {
				continue
			}
			plan := groups[point]
			if plan == nil {
				plan = &batchGroupPlan{point: point, participants: nil, seen: map[string]bool{}}
				groups[point] = plan
				order = append(order, point)
			}
			if !plan.seen[rule.Name] {
				plan.seen[rule.Name] = true
				plan.participants = append(plan.participants, batchParticipant{ruleName: rule.Name, intent: rule.Intent})
			}
		}
	}
	return groups, order
}

// contextFetch is the raw result of one conversation-context fetch. errClass is
// non-empty on a fetch failure, and each group applies its own point's on-error
// policy to it.
type contextFetch struct {
	json     string
	errClass string
}

// groupContextParams derives the context fetch inputs from a point, or reports that
// the point requests no context. The workspace and session come from the hook
// fields the point names.
func groupContextParams(point config.InferencePoint, fields *FieldSet) (contextParams, bool) {
	if point.ContextEndpoint == "" {
		var none contextParams
		return none, false
	}
	return contextParams{
		endpoint:        point.ContextEndpoint,
		workspace:       fields.String(config.CompileFieldSelector(point.ContextWorkspaceField)),
		session:         fields.String(config.CompileFieldSelector(point.ContextSessionField)),
		turnBudget:      point.ContextTurnBudget,
		maxCharsPerTurn: point.ContextMaxCharsPerTurn,
	}, true
}

// prefetchGroupContexts fetches each distinct context configuration once, so
// several points that share the same endpoint and selectors reuse one fetch while
// points with a different endpoint fetch separately. Fetching before the group
// goroutines run keeps the returned map read-only during the concurrent calls.
func (runtime *InferRuntime) prefetchGroupContexts(
	ctx context.Context,
	order []config.InferencePoint,
	fields *FieldSet,
) map[contextParams]contextFetch {
	fetched := map[contextParams]contextFetch{}
	for _, point := range order {
		params, wantsContext := groupContextParams(point, fields)
		if !wantsContext {
			continue
		}
		if _, done := fetched[params]; done {
			continue
		}
		contextJSON, errClass := runtime.fetchContextJSON(ctx, params)
		fetched[params] = contextFetch{json: contextJSON, errClass: errClass}
	}
	return fetched
}

// groupContext returns the context JSON for one point and whether a fetch failure
// should be treated as an error, applying that point's own on-error policy to the
// fetch made for its own endpoint.
func groupContext(point config.InferencePoint, fields *FieldSet, fetched map[contextParams]contextFetch) (string, bool) {
	params, wantsContext := groupContextParams(point, fields)
	if !wantsContext {
		return "", false
	}
	fetch := fetched[params]
	if fetch.errClass != "" && point.ContextOnError == config.OnErrorClosed {
		return "", true
	}
	return fetch.json, false
}

// evaluateBatchGroup issues one inference call judging the command against every
// participant rule and returns each rule's decision. A transport, status, or parse
// failure, or a context error under a closed context policy, marks every
// participant errored so the read site applies each entry's on_error.
func (runtime *InferRuntime) evaluateBatchGroup(
	ctx context.Context,
	plan *batchGroupPlan,
	command string,
	contextJSON string,
	contextErrored bool,
) *batchGroupResult {
	startedAt := runtime.now()
	prompt := buildBatchPrompt(plan.participants)
	inputJSON := marshalBatchInput(prompt, command, plan.point.Model, contextJSON)
	fail := func(code string) *batchGroupResult {
		decisions := make(map[string]batchRuleDecision, len(plan.participants))
		for _, participant := range plan.participants {
			decisions[participant.ruleName] = batchRuleDecision{block: false, errored: true, errorCode: code}
		}
		return &batchGroupResult{
			model:       plan.point.Model,
			inputJSON:   inputJSON,
			upstream:    boundedUpstreamMetadata(nil),
			startedAt:   startedAt,
			completedAt: runtime.now(),
			decisions:   decisions,
		}
	}
	if contextErrored {
		return fail("context_unavailable")
	}
	timeout := time.Duration(plan.point.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultPointTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := runtime.inferenceClient(plan.point.Endpoint)
	if err != nil {
		return fail("invalid_endpoint")
	}
	reply, err := client.Infer(callCtx, &inferencepb.InferRequest{
		Prompt:            prompt,
		Input:             command,
		OutputSchema:      batchDecisionOutputSchema,
		Context:           contextJSON,
		Model:             plan.point.Model,
		GenerationOptions: pointGenerationOptions(plan.point),
	})
	if err != nil {
		return fail(grpcErrorClass(err))
	}
	if reply == nil || reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
		return fail("non_complete")
	}
	parsed, ok := parseBatchDecisions(reply.GetOutputJson())
	if !ok {
		return fail("invalid_response")
	}
	return &batchGroupResult{
		model:       plan.point.Model,
		inputJSON:   inputJSON,
		upstream:    boundedUpstreamMetadata(reply.GetMetadata()),
		startedAt:   startedAt,
		completedAt: runtime.now(),
		decisions:   batchDecisionsForParticipants(plan.participants, parsed),
	}
}

// batchDecisionsForParticipants maps each participant to its parsed decision,
// marking a rule the model omitted as errored so the read site fails it per policy.
func batchDecisionsForParticipants(participants []batchParticipant, parsed map[string]string) map[string]batchRuleDecision {
	decisions := make(map[string]batchRuleDecision, len(participants))
	for _, participant := range participants {
		decision, found := parsed[participant.ruleName]
		if !found {
			decisions[participant.ruleName] = batchRuleDecision{block: false, errored: true, errorCode: "missing_decision"}
			continue
		}
		decisions[participant.ruleName] = batchRuleDecision{block: decision == "block", errored: false, errorCode: ""}
	}
	return decisions
}

// buildBatchPrompt renders the judging instruction and the per-rule intents, each
// tagged with a stable rule_id the model echoes in its array reply.
func buildBatchPrompt(participants []batchParticipant) string {
	var builder strings.Builder
	builder.WriteString("You are a security guard reviewing one shell command. ")
	builder.WriteString("Judge the command independently against each rule below. ")
	builder.WriteString(`Return a JSON object {"decisions":[{"rule_id":"<id>","decision":"allow"|"block"}]} `)
	builder.WriteString("with exactly one entry per rule, using the rule_id values given. ")
	builder.WriteString("Recent conversation context, when provided, tells you what the user is doing.\n\nRules:\n")
	for _, participant := range participants {
		builder.WriteString("- rule_id: ")
		builder.WriteString(participant.ruleName)
		builder.WriteString("\n  ")
		builder.WriteString(strings.ReplaceAll(participant.intent, "\n", " "))
		builder.WriteString("\n")
	}
	return builder.String()
}

// marshalBatchInput records the batch prompt, the command, the model, and the
// conversation context as the layer input, so the recorded layer shows what the
// judge saw.
func marshalBatchInput(prompt, command, model, contextJSON string) string {
	encoded, err := json.Marshal(struct {
		Prompt  string `json:"prompt"`
		Input   string `json:"input"`
		Model   string `json:"model"`
		Context string `json:"context,omitempty"`
	}{Prompt: prompt, Input: command, Model: model, Context: contextJSON})
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// batchRuleOutputJSON renders one rule's recorded output, matching the single
// decision shape the individual path records, or an empty object on error.
func batchRuleOutputJSON(decision batchRuleDecision) string {
	if decision.errored {
		return "{}"
	}
	if decision.block {
		return `{"decision":"block"}`
	}
	return `{"decision":"allow"}`
}

// parseBatchDecisions reads the array reply into a rule_id to decision map. It
// returns ok=false only when the reply is not valid JSON; an entry with an invalid
// decision is dropped so its rule reads as a missing decision.
func parseBatchDecisions(outputJSON string) (map[string]string, bool) {
	var decoded struct {
		Decisions []struct {
			RuleID   string `json:"rule_id"`
			Decision string `json:"decision"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(outputJSON), &decoded); err != nil {
		return nil, false
	}
	out := make(map[string]string, len(decoded.Decisions))
	seen := make(map[string]int, len(decoded.Decisions))
	for _, decision := range decoded.Decisions {
		if decision.Decision != "allow" && decision.Decision != "block" {
			continue
		}
		seen[decision.RuleID]++
		out[decision.RuleID] = decision.Decision
	}
	// A rule the model answered more than once is ambiguous (it may conflict), so
	// drop it and let the rule read as a missing decision and apply its on_error,
	// rather than letting the last duplicate silently win.
	for ruleID, count := range seen {
		if count > 1 {
			delete(out, ruleID)
		}
	}
	return out, true
}
