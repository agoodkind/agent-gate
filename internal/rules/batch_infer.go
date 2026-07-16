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

// blocksOnlyOutputSchema is the JSON Schema a batch inference call answers with:
// only the rule_ids the model decides to block. A rule absent from the list is
// an allow, so a normal command answers {"block":[]}.
const blocksOnlyOutputSchema = `{"type":"object","properties":{"block":{"type":"array","items":{"type":"string"}}},"required":["block"],"additionalProperties":false}`

// defaultJudgeTranscriptTimeout bounds the once-per-command transcript fetch when
// the judge config leaves the timeout unset, so a hung clyde stream cannot stall
// the gated tool call.
const defaultJudgeTranscriptTimeout = 1500 * time.Millisecond

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
// the eval matrix reads. It fetches the conversation transcript once per command
// under a bounded deadline, builds the judge-input panel once, and shares that one
// input across every group so all rules of one command judge on the same
// directory, verbatim call, structural parse, and conversation. It returns nil
// when no rule opts in.
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
	judgeInput := buildJudgeInput(*fields, runtime.fetchJudgeTranscript(ctx, fields))
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
			result := runtime.evaluateBatchGroup(ctx, plan, judgeInput)
			mu.Lock()
			memo.groups[point] = result
			mu.Unlock()
		}(point, plan)
	}
	wg.Wait()
	return memo
}

// fetchJudgeTranscript fetches the conversation transcript tail once per command
// under a bounded deadline and fails open to an empty tail. It returns "" when the
// judge sets no transcript endpoint, when the hook carries no conversation id, or
// when the fetch errors, so a transcript outage never blocks or errors the judge:
// the judge still runs on the directory, command, and structural parse.
func (runtime *InferRuntime) fetchJudgeTranscript(ctx context.Context, fields *FieldSet) string {
	settings := runtime.judgeTranscriptConfig()
	if settings.endpoint == "" || settings.maxTokens <= 0 {
		return ""
	}
	conversationID := strings.TrimSpace(fields.ConversationID)
	if conversationID == "" {
		return ""
	}
	timeout := settings.timeout
	if timeout <= 0 {
		timeout = defaultJudgeTranscriptTimeout
	}
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	tail, errClass := runtime.fetchTranscriptTail(fetchCtx, transcriptParams{
		endpoint:       settings.endpoint,
		conversationID: conversationID,
		tokenModel:     settings.tokenModel,
		maxTokens:      settings.maxTokens,
	})
	if errClass != "" {
		return ""
	}
	return tail
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

// evaluateBatchGroup issues one inference call judging the judge-input panel
// against every participant rule and returns each rule's decision. The prompt is
// the rule intents (the stable, cacheable prefix), the input is the per-command
// judge-input panel, and the context is empty because the conversation now lives
// inside the input. A transport, status, or parse failure marks every participant
// errored so the read site applies each entry's on_error.
func (runtime *InferRuntime) evaluateBatchGroup(
	ctx context.Context,
	plan *batchGroupPlan,
	judgeInput string,
) *batchGroupResult {
	startedAt := runtime.now()
	prompt := buildBatchPrompt(plan.participants)
	inputJSON := marshalBatchInput(prompt, judgeInput, plan.point.Model)
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
		Input:             judgeInput,
		OutputSchema:      blocksOnlyOutputSchema,
		Context:           "",
		Model:             plan.point.Model,
		GenerationOptions: pointGenerationOptions(plan.point),
	})
	if err != nil {
		return fail(grpcErrorClass(err))
	}
	if reply == nil || reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
		return fail("non_complete")
	}
	blockSet, ok := parseBlockList(reply.GetOutputJson())
	if !ok {
		return fail("invalid_response")
	}
	return &batchGroupResult{
		model:       plan.point.Model,
		inputJSON:   inputJSON,
		upstream:    boundedUpstreamMetadata(reply.GetMetadata()),
		startedAt:   startedAt,
		completedAt: runtime.now(),
		decisions:   batchDecisionsFromBlockList(plan.participants, blockSet),
	}
}

// batchDecisionsFromBlockList maps each participant to its decision: a rule named
// in the block set blocks, and a rule absent from the set allows. Absence means
// allow now, so an omitted rule is never errored. Rule ids in the set that are not
// participants are ignored, since only participants are iterated.
func batchDecisionsFromBlockList(participants []batchParticipant, blockSet map[string]bool) map[string]batchRuleDecision {
	decisions := make(map[string]batchRuleDecision, len(participants))
	for _, participant := range participants {
		decisions[participant.ruleName] = batchRuleDecision{
			block:     blockSet[participant.ruleName],
			errored:   false,
			errorCode: "",
		}
	}
	return decisions
}

// buildBatchPrompt renders the judging instruction and the per-rule intents, each
// tagged with a stable rule_id. It is the stable, byte-identical-across-calls
// prefix a model provider caches, so it carries no per-command detail: the
// directory, verbatim call, structural parse, and conversation ride in the input.
// The model returns only the rule_ids it blocks, so a normal command answers with
// an empty list rather than one decision per rule.
func buildBatchPrompt(participants []batchParticipant) string {
	var builder strings.Builder
	builder.WriteString("You are a security guard reviewing one tool call. ")
	builder.WriteString("Judge the tool call independently against each rule below. ")
	builder.WriteString(`Return a JSON object {"block":["<rule_id>",...]} listing only the rule_id values you decide to block, and an empty list when nothing should be blocked. `)
	builder.WriteString("Use the exact rule_id values given.\n\nRules:\n")
	for _, participant := range participants {
		builder.WriteString("- rule_id: ")
		builder.WriteString(participant.ruleName)
		builder.WriteString("\n  ")
		builder.WriteString(strings.ReplaceAll(participant.intent, "\n", " "))
		builder.WriteString("\n")
	}
	return builder.String()
}

// marshalBatchInput records the batch prompt, the judge-input panel, and the model
// as the layer input, so the recorded layer shows what the judge saw. The
// conversation now rides inside the judge-input panel, so there is no separate
// context field.
func marshalBatchInput(prompt, input, model string) string {
	encoded, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
		Input  string `json:"input"`
		Model  string `json:"model"`
	}{Prompt: prompt, Input: input, Model: model})
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

// parseBlockList reads the blocks-only reply into the set of rule_ids to block. It
// returns ok=false only when the reply is not valid JSON, in which case the caller
// errors every participant so each applies its on_error. A repeated rule_id is
// harmless because the set already collapses duplicates to one block entry.
func parseBlockList(outputJSON string) (map[string]bool, bool) {
	var decoded struct {
		Block []string `json:"block"`
	}
	if err := json.Unmarshal([]byte(outputJSON), &decoded); err != nil {
		return nil, false
	}
	blockSet := make(map[string]bool, len(decoded.Block))
	for _, ruleID := range decoded.Block {
		blockSet[ruleID] = true
	}
	return blockSet, true
}
