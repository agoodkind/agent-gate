package rules

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

const (
	traceStatusComplete = "complete"
	traceStatusError    = "error"
	traceStatusSkipped  = "skipped"

	skipEventNotApplicable     = "event_not_applicable"
	skipDisabledByEnv          = "disabled_by_env"
	skipPriorConditionNonmatch = "prior_condition_nonmatch"
)

// VerifiedProvenance contains identities derived only from the local request.
type VerifiedProvenance struct {
	RequestedModel           string `json:"requested_model,omitempty"`
	EndpointHash             string `json:"endpoint_hash,omitempty"`
	CacheKeyHash             string `json:"cache_key_hash,omitempty"`
	InputHash                string `json:"input_hash,omitempty"`
	PromptSHA256             string `json:"prompt_sha256,omitempty"`
	SchemaSHA256             string `json:"schema_sha256,omitempty"`
	ReportedPromptHashStatus string `json:"reported_prompt_hash_status"`
	ReportedSchemaHashStatus string `json:"reported_schema_hash_status"`
}

// UpstreamMetadataStatus describes whether bounded raw metadata is available.
type UpstreamMetadataStatus string

const (
	// UpstreamMetadataPresent means bounded raw metadata is available.
	UpstreamMetadataPresent UpstreamMetadataStatus = "present"
	// UpstreamMetadataAbsent means the inference reply had no metadata.
	UpstreamMetadataAbsent UpstreamMetadataStatus = "absent"
	// UpstreamMetadataOmittedMalformed means protobuf JSON encoding failed.
	UpstreamMetadataOmittedMalformed UpstreamMetadataStatus = "omitted_malformed"
	// UpstreamMetadataOmittedOversize means encoded metadata exceeded the limit.
	UpstreamMetadataOmittedOversize UpstreamMetadataStatus = "omitted_oversize"
)

// UpstreamMetadata is a bounded, explicitly untrusted inference-reply snapshot.
type UpstreamMetadata struct {
	Source string                 `json:"source"`
	Trust  string                 `json:"trust"`
	Status UpstreamMetadataStatus `json:"status"`
	Raw    json.RawMessage        `json:"raw,omitempty"`
}

// DecisionTrace contains the deterministic rule decision and ordered optional layers.
type DecisionTrace struct {
	Deterministic DeterministicTrace
	Layers        []LayerTrace
}

// DeterministicTrace records the exact deterministic rule-engine boundary.
type DeterministicTrace struct {
	StartedAt      time.Time
	CompletedAt    time.Time
	InputJSON      json.RawMessage
	OutputJSON     json.RawMessage
	InputHash      string
	OutputHash     string
	ServiceName    string
	ServiceVersion string
	CheckedRules   []RuleDecision
}

// RuleDecision records one configured rule in declaration order.
type RuleDecision struct {
	RuleName   string `json:"rule_name"`
	Status     string `json:"status"`
	SkipReason string `json:"skip_reason,omitempty"`
	Matched    bool   `json:"matched"`
}

// LayerTrace records one rich context or inference boundary for ledger persistence.
type LayerTrace struct {
	RuleName           string
	ConditionIndex     int
	LayerName          string
	Kind               string
	Status             string
	Outcome            string
	SkipReason         string
	ParentTraceIndex   *int
	StartedAt          time.Time
	CompletedAt        time.Time
	InputReference     string
	InputJSON          json.RawMessage
	OutputJSON         json.RawMessage
	InputHash          string
	OutputHash         string
	ServiceName        string
	ServiceVersion     string
	VerifiedProvenance VerifiedProvenance
	CacheStatus        string
	CacheKeyHash       string
	CacheEntryVersion  *int64
	CacheExpiresAt     *time.Time
	UpstreamMetadata   UpstreamMetadata
	ErrorCode          string
	ErrorMessage       string
	RetryCount         int
}

func newLayerTrace(ruleName string, conditionIndex int, layerName string, kind string) LayerTrace {
	return LayerTrace{
		RuleName: ruleName, ConditionIndex: conditionIndex, LayerName: layerName,
		Kind: kind, Status: "", Outcome: "", SkipReason: "", ParentTraceIndex: nil,
		StartedAt: time.Time{}, CompletedAt: time.Time{}, InputReference: "",
		InputJSON: nil, OutputJSON: nil, InputHash: "", OutputHash: "",
		ServiceName: "", ServiceVersion: "", VerifiedProvenance: emptyVerifiedProvenance(),
		CacheStatus:  "",
		CacheKeyHash: "", CacheEntryVersion: nil, CacheExpiresAt: nil,
		UpstreamMetadata: emptyUpstreamMetadata(), ErrorCode: "", ErrorMessage: "",
		RetryCount: 0,
	}
}

func emptyVerifiedProvenance() VerifiedProvenance {
	return VerifiedProvenance{
		RequestedModel: "", EndpointHash: "", CacheKeyHash: "", InputHash: "",
		PromptSHA256: "", SchemaSHA256: "", ReportedPromptHashStatus: "absent",
		ReportedSchemaHashStatus: "absent",
	}
}

func emptyUpstreamMetadata() UpstreamMetadata {
	return UpstreamMetadata{
		Source: "inference_reply", Trust: "untrusted", Status: UpstreamMetadataAbsent,
		Raw: nil,
	}
}

// DetailedEvaluation returns compatibility violations plus the complete trace.
type DetailedEvaluation struct {
	Violations []Violation
	Trace      DecisionTrace
}

var decisionTraceNow = time.Now

// EvaluateAllDetailed returns rule violations and the exact rich decision trace.
func EvaluateAllDetailed(
	ctx context.Context,
	system string,
	eventName string,
	fields FieldSet,
	rulesSlice []config.Rule,
	getenv func(string) string,
	inputJSON json.RawMessage,
	serviceVersion string,
) DetailedEvaluation {
	startedAt := decisionTraceNow().UTC()
	collector := &richTraceCollector{
		mu: sync.Mutex{}, layers: make([]LayerTrace, 0),
		seen: make(map[traceIdentity]struct{}),
	}
	evalCtx := withCommandEnvironment(ctx, getenv)
	evalCtx = withRichTraceCollector(evalCtx, collector)
	collectPreSkippedInferenceLayers(evalCtx, rulesSlice, system, eventName, getenv)
	violations := evaluateAll(evalCtx, system, eventName, fields, rulesSlice, getenv)
	decisions := deterministicRuleDecisions(rulesSlice, system, eventName, getenv, violations)
	outputJSON := deterministicOutputJSON(system, eventName, decisions, violations)
	completedAt := decisionTraceNow().UTC()
	inputCopy := append(json.RawMessage(nil), inputJSON...)
	if len(inputCopy) == 0 {
		inputCopy = json.RawMessage(`{}`)
	}
	return DetailedEvaluation{
		Violations: violations,
		Trace: DecisionTrace{
			Deterministic: DeterministicTrace{
				StartedAt: startedAt, CompletedAt: completedAt,
				InputJSON: inputCopy, OutputJSON: outputJSON,
				InputHash: traceJSONHash(inputCopy), OutputHash: traceJSONHash(outputJSON),
				ServiceName: "agent-gate", ServiceVersion: serviceVersion,
				CheckedRules: decisions,
			},
			Layers: collector.orderedSnapshot(rulesSlice),
		},
	}
}

type richTraceCollector struct {
	mu     sync.Mutex
	layers []LayerTrace
	seen   map[traceIdentity]struct{}
}

type traceIdentity struct {
	ruleName       string
	conditionIndex int
	kind           string
}

type richTraceCollectorKey struct{}

func withRichTraceCollector(ctx context.Context, collector *richTraceCollector) context.Context {
	return context.WithValue(ctx, richTraceCollectorKey{}, collector)
}

func richTraceCollectorFromContext(ctx context.Context) *richTraceCollector {
	collector, _ := ctx.Value(richTraceCollectorKey{}).(*richTraceCollector)
	return collector
}

func (collector *richTraceCollector) collect(layer LayerTrace) {
	if collector == nil {
		return
	}
	identity := traceIdentity{ruleName: layer.RuleName, conditionIndex: layer.ConditionIndex, kind: layer.Kind}
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if _, exists := collector.seen[identity]; exists {
		return
	}
	collector.seen[identity] = struct{}{}
	collector.layers = append(collector.layers, cloneLayerTrace(layer))
}

func (collector *richTraceCollector) snapshot() []LayerTrace {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	layers := make([]LayerTrace, len(collector.layers))
	for i := range collector.layers {
		layers[i] = cloneLayerTrace(collector.layers[i])
	}
	return layers
}

func (collector *richTraceCollector) orderedSnapshot(rulesSlice []config.Rule) []LayerTrace {
	layers := collector.snapshot()
	ruleOrder := make(map[string]int, len(rulesSlice))
	for i := range rulesSlice {
		ruleOrder[rulesSlice[i].Name] = i
	}
	sort.SliceStable(layers, func(i int, j int) bool {
		left := layers[i]
		right := layers[j]
		if ruleOrder[left.RuleName] != ruleOrder[right.RuleName] {
			return ruleOrder[left.RuleName] < ruleOrder[right.RuleName]
		}
		if left.ConditionIndex != right.ConditionIndex {
			return left.ConditionIndex < right.ConditionIndex
		}
		return left.Kind == "context" && right.Kind == "inference"
	})
	contextIndexes := make(map[traceIdentity]int)
	for i := range layers {
		layer := &layers[i]
		parent := 0
		if layer.Kind == "context" {
			layer.ParentTraceIndex = &parent
			contextIndexes[traceIdentity{
				ruleName: layer.RuleName, conditionIndex: layer.ConditionIndex, kind: "context",
			}] = i + 1
			continue
		}
		if contextIndex, ok := contextIndexes[traceIdentity{
			ruleName: layer.RuleName, conditionIndex: layer.ConditionIndex, kind: "context",
		}]; ok {
			parent = contextIndex
		}
		layer.ParentTraceIndex = &parent
	}
	return layers
}

func cloneLayerTrace(layer LayerTrace) LayerTrace {
	layer.InputJSON = append(json.RawMessage(nil), layer.InputJSON...)
	layer.OutputJSON = append(json.RawMessage(nil), layer.OutputJSON...)
	layer.UpstreamMetadata.Raw = append(json.RawMessage(nil), layer.UpstreamMetadata.Raw...)
	if layer.ParentTraceIndex != nil {
		parent := *layer.ParentTraceIndex
		layer.ParentTraceIndex = &parent
	}
	if layer.CacheEntryVersion != nil {
		version := *layer.CacheEntryVersion
		layer.CacheEntryVersion = &version
	}
	if layer.CacheExpiresAt != nil {
		expiresAt := *layer.CacheExpiresAt
		layer.CacheExpiresAt = &expiresAt
	}
	return layer
}

func traceJSONHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

type deterministicOutput struct {
	Rules                      []deterministicOutputRule `json:"rules"`
	ProviderBlockingCapability string                    `json:"provider_blocking_capability"`
	Result                     string                    `json:"result"`
}

type deterministicOutputRule struct {
	RuleDecision
	ViolationIdentities []violationIdentity `json:"violation_identities"`
}

type violationIdentity struct {
	RuleName  string `json:"rule_name"`
	FieldPath string `json:"field_path,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	Start     int    `json:"start"`
	End       int    `json:"end"`
}

type providerEvent string

const (
	providerClaudePreTool       providerEvent = "claude\x00PreToolUse"
	providerClaudePermission    providerEvent = "claude\x00PermissionRequest"
	providerClaudePrompt        providerEvent = "claude\x00UserPromptSubmit"
	providerCodexPreTool        providerEvent = "codex\x00PreToolUse"
	providerCodexPermission     providerEvent = "codex\x00PermissionRequest"
	providerCodexPrompt         providerEvent = "codex\x00UserPromptSubmit"
	providerCursorPreTool       providerEvent = "cursor\x00preToolUse"
	providerCursorBeforeShell   providerEvent = "cursor\x00beforeShellExecution"
	providerCursorBeforeMCP     providerEvent = "cursor\x00beforeMCPExecution"
	providerCursorBeforeRead    providerEvent = "cursor\x00beforeReadFile"
	providerCursorBeforePrompt  providerEvent = "cursor\x00beforeSubmitPrompt"
	providerCursorBeforeTabRead providerEvent = "cursor\x00beforeTabFileRead"
	providerGeminiBeforeTool    providerEvent = "gemini\x00BeforeTool"
	providerCodexPostTool       providerEvent = "codex\x00PostToolUse"
	providerCursorPostTool      providerEvent = "cursor\x00postToolUse"
)

func deterministicRuleDecisions(
	rulesSlice []config.Rule,
	system string,
	eventName string,
	getenv func(string) string,
	violations []Violation,
) []RuleDecision {
	matchedRules := make(map[string]bool)
	for _, violation := range violations {
		matchedRules[violation.RuleName] = true
	}
	decisions := make([]RuleDecision, 0, len(rulesSlice))
	for i := range rulesSlice {
		rule := &rulesSlice[i]
		decision := RuleDecision{
			RuleName: rule.Name, Status: "nonmatched", SkipReason: "", Matched: false,
		}
		switch {
		case !appliesToEvent(rule, system, eventName):
			decision.Status = traceStatusSkipped
			decision.SkipReason = skipEventNotApplicable
		case envGuardFires(getenv, rule.DisableIfEnv):
			decision.Status = traceStatusSkipped
			decision.SkipReason = skipDisabledByEnv
		case matchedRules[rule.Name]:
			decision.Status = "matched"
			decision.Matched = true
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func deterministicOutputJSON(
	system string,
	eventName string,
	decisions []RuleDecision,
	violations []Violation,
) json.RawMessage {
	outputRules := make([]deterministicOutputRule, len(decisions))
	for i := range decisions {
		outputRules[i].RuleDecision = decisions[i]
		for _, violation := range violations {
			if violation.RuleName != decisions[i].RuleName {
				continue
			}
			outputRules[i].ViolationIdentities = append(
				outputRules[i].ViolationIdentities,
				violationIdentity{
					RuleName:  violation.RuleName,
					FieldPath: violation.FieldPath,
					FilePath:  violation.FilePath,
					Start:     violation.Start,
					End:       violation.End,
				},
			)
		}
		if outputRules[i].ViolationIdentities == nil {
			outputRules[i].ViolationIdentities = make([]violationIdentity, 0)
		}
	}
	result := "allow"
	if len(violations) > 0 {
		result = "block"
	}
	encoded, err := json.Marshal(deterministicOutput{
		Rules:                      outputRules,
		ProviderBlockingCapability: providerBlockingCapability(system, eventName),
		Result:                     result,
	})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func providerBlockingCapability(system string, eventName string) string {
	switch providerEvent(system + "\x00" + eventName) {
	case providerClaudePreTool, providerClaudePermission, providerClaudePrompt,
		providerCodexPreTool, providerCodexPermission, providerCodexPrompt,
		providerCursorPreTool, providerCursorBeforeShell, providerCursorBeforeMCP,
		providerCursorBeforeRead, providerCursorBeforePrompt, providerCursorBeforeTabRead,
		providerGeminiBeforeTool:
		return "block"
	case providerCodexPostTool, providerCursorPostTool:
		return "substitute"
	default:
		return "observe"
	}
}

func collectPreSkippedInferenceLayers(
	ctx context.Context,
	rulesSlice []config.Rule,
	system string,
	eventName string,
	getenv func(string) string,
) {
	for i := range rulesSlice {
		rule := &rulesSlice[i]
		reason := ""
		if !appliesToEvent(rule, system, eventName) {
			reason = skipEventNotApplicable
		} else if envGuardFires(getenv, rule.DisableIfEnv) {
			reason = skipDisabledByEnv
		}
		if reason == "" {
			continue
		}
		for conditionIndex := range rule.Conditions {
			condition := &rule.Conditions[conditionIndex]
			if conditionKind(condition) == config.ConditionKindInfer {
				collectSkippedInferenceCondition(ctx, rule, conditionIndex, condition, reason)
			}
		}
	}
}

func collectSkippedInferenceCondition(
	ctx context.Context,
	rule *config.Rule,
	conditionIndex int,
	condition *config.Condition,
	reason string,
) {
	collector := richTraceCollectorFromContext(ctx)
	if collector == nil {
		return
	}
	parent := 0
	if condition.ContextSource != "" {
		contextLayer := newLayerTrace(
			rule.Name, conditionIndex, rule.Name+"/"+condition.LayerName+"/context", "context",
		)
		contextLayer.Status = traceStatusSkipped
		contextLayer.SkipReason = reason
		contextLayer.ParentTraceIndex = &parent
		contextLayer.InputJSON = json.RawMessage(`{}`)
		contextLayer.OutputJSON = json.RawMessage(`{}`)
		contextLayer.InputHash = traceJSONHash(contextLayer.InputJSON)
		contextLayer.OutputHash = traceJSONHash(contextLayer.OutputJSON)
		collector.collect(contextLayer)
		parent = collector.traceIndex(rule.Name, conditionIndex, "context")
	}
	inferenceLayer := newLayerTrace(rule.Name, conditionIndex, condition.LayerName, "inference")
	inferenceLayer.Status = traceStatusSkipped
	inferenceLayer.SkipReason = reason
	inferenceLayer.ParentTraceIndex = &parent
	inferenceLayer.InputJSON = json.RawMessage(`{}`)
	inferenceLayer.OutputJSON = json.RawMessage(`{}`)
	inferenceLayer.InputHash = traceJSONHash(inferenceLayer.InputJSON)
	inferenceLayer.OutputHash = traceJSONHash(inferenceLayer.OutputJSON)
	inferenceLayer.VerifiedProvenance.RequestedModel = condition.Model
	collector.collect(inferenceLayer)
}

func (collector *richTraceCollector) traceIndex(ruleName string, conditionIndex int, kind string) int {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	for i := range collector.layers {
		layer := &collector.layers[i]
		if layer.RuleName == ruleName && layer.ConditionIndex == conditionIndex && layer.Kind == kind {
			return i + 1
		}
	}
	return 0
}
