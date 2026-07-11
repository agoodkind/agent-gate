package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/rules"
)

type evaluationRecorder interface {
	RecordCompleted(context.Context, evaluation.Record) error
	CommitHotEvaluation(
		context.Context, string, int64, bool, evaluation.Record,
	) error
	CommitDeferredEvaluation(
		context.Context, intake.DeferredClaim, evaluation.Record,
	) error
}

const hotEvaluationAttempt = 1

type hotEvaluationRecordInput struct {
	ReceiptID       int64
	EventID         string
	Intake          intake.Record
	ConfigHash      string
	EngineVersion   string
	EngineCommit    string
	EngineBuildHash string
	StartedAt       time.Time
	CompletedAt     time.Time
	Result          hook.HotEvaluation
	SystemError     string
	ErrorMessage    string
}

type deferredEvaluationRecordInput struct {
	ReceiptID       int64
	EventID         string
	Intake          intake.Record
	Mode            string
	Attempt         int
	ConfigHash      string
	EngineVersion   string
	EngineCommit    string
	EngineBuildHash string
	StartedAt       time.Time
	CompletedAt     time.Time
	Event           hook.DeferredAuditEvent
}

type layerMetadataV2 struct {
	SchemaVersion      int                      `json:"schema_version"`
	RuleName           string                   `json:"rule_name,omitempty"`
	ConditionIndex     int                      `json:"condition_index,omitempty"`
	SkipReason         string                   `json:"skip_reason,omitempty"`
	VerifiedProvenance rules.VerifiedProvenance `json:"verified_provenance"`
	UpstreamMetadata   rules.UpstreamMetadata   `json:"upstream_metadata"`
	GenerationOptions  json.RawMessage          `json:"generation_options,omitempty"`
}

type finalLayerOutput struct {
	Verdict            string   `json:"verdict"`
	Source             string   `json:"source"`
	EnforcementAction  string   `json:"enforcement_action"`
	Enforced           bool     `json:"enforced"`
	BlockingRuleNames  []string `json:"blocking_rule_names"`
	AuditOnlyRuleNames []string `json:"audit_only_rule_names"`
	ExitCode           int      `json:"exit_code"`
	StdoutHash         string   `json:"stdout_hash"`
	StderrHash         string   `json:"stderr_hash"`
}

type finalDisposition struct {
	verdict           string
	source            string
	enforcementAction string
	enforced          bool
	status            string
}

type evaluationErrorJSON struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type deterministicLayerMetadata struct {
	SchemaVersion int                  `json:"schema_version"`
	CheckedRules  []rules.RuleDecision `json:"checked_rules"`
}

func buildHotEvaluationRecord(input hotEvaluationRecordInput) evaluation.Record {
	disposition := hotFinalDisposition(input.Result, input.SystemError)
	layers := make([]evaluation.Layer, 0, len(input.Result.Trace.Layers)+2)
	if input.SystemError == intakeParseFailed {
		layers = append(layers, payloadValidationLayer(input))
	} else {
		layers = append(layers, deterministicEvaluationLayer(input.Result.Trace.Deterministic))
		for i := range input.Result.Trace.Layers {
			layers = append(layers, richEvaluationLayer(i+1, input.Result.Trace.Layers[i]))
		}
	}
	finalLayer := hotFinalLayer(len(layers), input.Result, disposition, input.CompletedAt)
	layers = append(layers, finalLayer)
	errorJSON := json.RawMessage(`{}`)
	if input.SystemError != "" {
		errorJSON = marshalEvaluationError(evaluationErrorJSON{
			Code: input.SystemError, Message: input.ErrorMessage,
		})
	}
	latency := input.CompletedAt.Sub(input.StartedAt)
	latency = max(latency, 0)
	return evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: hotEvaluationID(input.ReceiptID),
			ReceiptID:    input.ReceiptID, EventID: input.EventID,
			Attempt: hotEvaluationAttempt, Mode: "hot", ConfigHash: input.ConfigHash,
			EngineVersion: input.EngineVersion, EngineCommit: input.EngineCommit,
			EngineBuildHash: input.EngineBuildHash, InputHash: evaluationHash(input.Intake.RawPayload),
			StartedAt: input.StartedAt, CompletedAt: input.CompletedAt,
			FinalVerdict: disposition.verdict, FinalSource: disposition.source,
			EnforcementAction: disposition.enforcementAction, Enforced: disposition.enforced,
			TotalLatencyUS: latency.Microseconds(), ErrorJSON: errorJSON,
		},
		Layers: layers,
		Labels: make([]evaluation.Label, 0),
	}
}

func payloadValidationLayer(input hotEvaluationRecordInput) evaluation.Layer {
	outputJSON := json.RawMessage(`{"valid":false}`)
	return evaluation.Layer{
		LayerIndex: 0, ParentLayerIndex: nil, Kind: "validation", Name: "payload-parse",
		Status: "error", Outcome: "", InputReference: "intake.raw_payload",
		InputJSON: json.RawMessage(`{}`), InputHash: evaluationHash(input.Intake.RawPayload),
		OutputHash: evaluationHash(outputJSON), OutputJSON: outputJSON,
		MetadataJSON: json.RawMessage(`{"schema_version":1}`), StartedAt: input.StartedAt,
		CompletedAt: input.CompletedAt,
		LatencyUS:   max(input.CompletedAt.Sub(input.StartedAt), 0).Microseconds(),
		ServiceName: "agent-gate", ServiceVersion: input.EngineVersion,
		ModelName: "", ModelVersion: "", PromptHash: "", SchemaHash: "",
		CacheStatus: "", CacheKeyHash: "", CacheEntryVersion: nil, CacheExpiresAt: nil,
		ErrorCode: input.SystemError, ErrorMessage: input.ErrorMessage, RetryCount: 0,
	}
}

func buildDeferredEvaluationRecord(input deferredEvaluationRecordInput) evaluation.Record {
	trace := input.Event.Trace
	layers := make([]evaluation.Layer, 0, len(trace.Layers)+2)
	layers = append(layers, deterministicEvaluationLayer(trace.Deterministic))
	for i := range trace.Layers {
		layers = append(layers, richEvaluationLayer(i+1, trace.Layers[i]))
	}
	disposition := deferredFinalDisposition(input.Event)
	layers = append(layers, deferredFinalLayer(len(layers), input.Event, disposition, input.CompletedAt))
	latency := input.CompletedAt.Sub(input.StartedAt)
	latency = max(latency, 0)
	return evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: evaluationID(input.ReceiptID, input.Mode, input.Attempt),
			ReceiptID:    input.ReceiptID, EventID: input.EventID, Attempt: input.Attempt,
			Mode: input.Mode, ConfigHash: input.ConfigHash, EngineVersion: input.EngineVersion,
			EngineCommit: input.EngineCommit, EngineBuildHash: input.EngineBuildHash,
			InputHash: evaluationHash(input.Intake.RawPayload), StartedAt: input.StartedAt,
			CompletedAt: input.CompletedAt, FinalVerdict: disposition.verdict,
			FinalSource: disposition.source, EnforcementAction: disposition.enforcementAction,
			Enforced: disposition.enforced, TotalLatencyUS: latency.Microseconds(),
			ErrorJSON: json.RawMessage(`{}`),
		},
		Layers: layers,
		Labels: make([]evaluation.Label, 0),
	}
}

func hotEvaluationID(receiptID int64) string {
	return evaluationID(receiptID, "hot", hotEvaluationAttempt)
}

func evaluationID(receiptID int64, mode string, attempt int) string {
	hash := sha256.New()
	writeEvaluationHashPart(hash, strconv.FormatInt(receiptID, 10))
	writeEvaluationHashPart(hash, mode)
	writeEvaluationHashPart(hash, strconv.Itoa(attempt))
	return "eval_" + hex.EncodeToString(hash.Sum(nil))
}

type evaluationHashWriter interface {
	Write([]byte) (int, error)
	Sum([]byte) []byte
}

func writeEvaluationHashPart(hash evaluationHashWriter, value string) {
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

func evaluationHash(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func deterministicEvaluationLayer(trace rules.DeterministicTrace) evaluation.Layer {
	latency := trace.CompletedAt.Sub(trace.StartedAt)
	latency = max(latency, 0)
	outcome := "nonmatch"
	for _, decision := range trace.CheckedRules {
		if decision.Matched {
			outcome = "match"
			break
		}
	}
	metadata := marshalDeterministicMetadata(deterministicLayerMetadata{
		SchemaVersion: 1, CheckedRules: trace.CheckedRules,
	})
	return evaluation.Layer{
		LayerIndex: 0, ParentLayerIndex: nil, Kind: "deterministic", Name: "rule-engine",
		Status: "complete", Outcome: outcome, InputReference: "intake.normalized_json",
		InputJSON: append(json.RawMessage(nil), trace.InputJSON...), InputHash: trace.InputHash,
		OutputHash: trace.OutputHash, OutputJSON: append(json.RawMessage(nil), trace.OutputJSON...),
		MetadataJSON: metadata, StartedAt: trace.StartedAt, CompletedAt: trace.CompletedAt,
		LatencyUS: latency.Microseconds(), ServiceName: trace.ServiceName,
		ServiceVersion: trace.ServiceVersion, ModelName: "", ModelVersion: "",
		PromptHash: "", SchemaHash: "", CacheStatus: "", CacheKeyHash: "",
		CacheEntryVersion: nil, CacheExpiresAt: nil, ErrorCode: "", ErrorMessage: "",
		RetryCount: 0,
	}
}

func richEvaluationLayer(layerIndex int, trace rules.LayerTrace) evaluation.Layer {
	latency := trace.CompletedAt.Sub(trace.StartedAt)
	latency = max(latency, 0)
	metadata := layerMetadataV2{
		SchemaVersion: 2, RuleName: trace.RuleName, ConditionIndex: trace.ConditionIndex,
		SkipReason: trace.SkipReason, VerifiedProvenance: trace.VerifiedProvenance,
		UpstreamMetadata:  trace.UpstreamMetadata,
		GenerationOptions: inferenceGenerationOptions(trace.InputJSON),
	}
	return evaluation.Layer{
		LayerIndex: layerIndex, ParentLayerIndex: cloneIntPointer(trace.ParentTraceIndex),
		Kind: trace.Kind, Name: trace.LayerName, Status: trace.Status, Outcome: trace.Outcome,
		InputReference: trace.InputReference, InputJSON: append(json.RawMessage(nil), trace.InputJSON...),
		InputHash: trace.InputHash, OutputHash: trace.OutputHash,
		OutputJSON:   append(json.RawMessage(nil), trace.OutputJSON...),
		MetadataJSON: marshalLayerMetadata(metadata), StartedAt: trace.StartedAt,
		CompletedAt: trace.CompletedAt, LatencyUS: latency.Microseconds(),
		ServiceName: trace.ServiceName, ServiceVersion: trace.ServiceVersion,
		ModelName: trace.VerifiedProvenance.RequestedModel, ModelVersion: "",
		PromptHash: trace.VerifiedProvenance.PromptSHA256,
		SchemaHash: trace.VerifiedProvenance.SchemaSHA256, CacheStatus: trace.CacheStatus,
		CacheKeyHash: trace.CacheKeyHash, CacheEntryVersion: cloneInt64Pointer(trace.CacheEntryVersion),
		CacheExpiresAt: cloneTimePointer(trace.CacheExpiresAt), ErrorCode: trace.ErrorCode,
		ErrorMessage: trace.ErrorMessage, RetryCount: trace.RetryCount,
	}
}

func hotFinalLayer(
	layerIndex int,
	result hook.HotEvaluation,
	disposition finalDisposition,
	completedAt time.Time,
) evaluation.Layer {
	parentIndex := lastAttemptedLayerIndex(result.Trace.Layers)
	outputJSON := marshalFinalLayerOutput(finalLayerOutput{
		Verdict: disposition.verdict, Source: disposition.source,
		EnforcementAction: disposition.enforcementAction, Enforced: disposition.enforced,
		BlockingRuleNames:  violationRuleNames(result.Deferred.BlockingViolations),
		AuditOnlyRuleNames: violationRuleNames(result.Deferred.AuditOnlyViolations),
		ExitCode:           result.ExitCode, StdoutHash: evaluationHash(result.Stdout),
		StderrHash: evaluationHash(result.Stderr),
	})
	return evaluation.Layer{
		LayerIndex: layerIndex, ParentLayerIndex: &parentIndex, Kind: "final", Name: "hook-response",
		Status: disposition.status, Outcome: "", InputReference: "evaluation.layers",
		InputJSON: json.RawMessage(`{}`), InputHash: evaluationHash([]byte(`{}`)),
		OutputHash: evaluationHash(outputJSON), OutputJSON: outputJSON,
		MetadataJSON: json.RawMessage(`{"schema_version":1}`), StartedAt: completedAt,
		CompletedAt: completedAt, LatencyUS: 0, ServiceName: "agent-gate",
		ServiceVersion: "", ModelName: "", ModelVersion: "", PromptHash: "",
		SchemaHash: "", CacheStatus: "", CacheKeyHash: "", CacheEntryVersion: nil,
		CacheExpiresAt: nil, ErrorCode: "", ErrorMessage: "", RetryCount: 0,
	}
}

func deferredFinalLayer(
	layerIndex int,
	event hook.DeferredAuditEvent,
	disposition finalDisposition,
	completedAt time.Time,
) evaluation.Layer {
	parentIndex := lastAttemptedLayerIndex(event.Trace.Layers)
	outputJSON := marshalFinalLayerOutput(finalLayerOutput{
		Verdict: disposition.verdict, Source: disposition.source,
		EnforcementAction: disposition.enforcementAction, Enforced: disposition.enforced,
		BlockingRuleNames:  violationRuleNames(event.BlockingViolations),
		AuditOnlyRuleNames: violationRuleNames(event.AuditOnlyViolations),
		ExitCode:           0, StdoutHash: evaluationHash(nil), StderrHash: evaluationHash(nil),
	})
	return evaluation.Layer{
		LayerIndex: layerIndex, ParentLayerIndex: &parentIndex, Kind: "final", Name: "audit-result",
		Status: disposition.status, Outcome: "", InputReference: "evaluation.layers",
		InputJSON: json.RawMessage(`{}`), InputHash: evaluationHash([]byte(`{}`)),
		OutputHash: evaluationHash(outputJSON), OutputJSON: outputJSON,
		MetadataJSON: json.RawMessage(`{"schema_version":1}`), StartedAt: completedAt,
		CompletedAt: completedAt, LatencyUS: 0, ServiceName: "agent-gate",
		ServiceVersion: "", ModelName: "", ModelVersion: "", PromptHash: "",
		SchemaHash: "", CacheStatus: "", CacheKeyHash: "", CacheEntryVersion: nil,
		CacheExpiresAt: nil, ErrorCode: "", ErrorMessage: "", RetryCount: 0,
	}
}

func deferredFinalDisposition(event hook.DeferredAuditEvent) finalDisposition {
	source := "deterministic"
	if hasAttemptedInference(event.Trace.Layers) {
		source = "inference"
	}
	if len(event.BlockingViolations) > 0 || len(event.AuditOnlyViolations) > 0 {
		return finalDisposition{
			verdict: "audit", source: source, enforcementAction: "audit",
			enforced: false, status: "complete",
		}
	}
	return finalDisposition{
		verdict: "allow", source: source, enforcementAction: "audit",
		enforced: false, status: "complete",
	}
}

func hotFinalDisposition(result hook.HotEvaluation, systemError string) finalDisposition {
	if systemError == intakeParseFailed {
		return finalDisposition{
			verdict: "error", source: "input_validation", enforcementAction: "reject_invalid",
			enforced: true, status: "error",
		}
	}
	if systemError != "" {
		return finalDisposition{
			verdict: "error", source: "system_error", enforcementAction: "fail_open",
			enforced: false, status: "error",
		}
	}
	source := "deterministic"
	if hasAttemptedInference(result.Trace.Layers) {
		source = "inference"
	}
	if len(result.Deferred.BlockingViolations) > 0 && result.Deferred.Decision == hook.ResponseDecisionBlock {
		enforcementAction := "deny"
		if hook.LookupCapability(
			result.Deferred.System, result.Deferred.EventName,
		) == hook.CapabilitySubstitute {
			enforcementAction = "substitute"
		}
		return finalDisposition{
			verdict: "block", source: source, enforcementAction: enforcementAction,
			enforced: hook.CanBlock(result.Deferred.System, result.Deferred.EventName),
			status:   "complete",
		}
	}
	if len(result.Deferred.AuditOnlyViolations) > 0 {
		return finalDisposition{
			verdict: "audit", source: "provider_capability", enforcementAction: "audit",
			enforced: false, status: "complete",
		}
	}
	return finalDisposition{
		verdict: "allow", source: source, enforcementAction: "allow",
		enforced: false, status: "complete",
	}
}

func hasAttemptedInference(layers []rules.LayerTrace) bool {
	for i := range layers {
		if layers[i].Kind == "inference" && layers[i].Status != "skipped" {
			return true
		}
	}
	return false
}

func lastAttemptedLayerIndex(layers []rules.LayerTrace) int {
	lastIndex := 0
	for i := range layers {
		if layers[i].Status != "skipped" {
			lastIndex = i + 1
		}
	}
	return lastIndex
}

func violationRuleNames(violations []rules.Violation) []string {
	names := make([]string, 0, len(violations))
	seen := make(map[string]bool)
	for _, violation := range violations {
		if seen[violation.RuleName] {
			continue
		}
		seen[violation.RuleName] = true
		names = append(names, violation.RuleName)
	}
	return names
}

func inferenceGenerationOptions(inputJSON json.RawMessage) json.RawMessage {
	var input struct {
		GenerationOptions json.RawMessage `json:"generation_options"`
	}
	if json.Unmarshal(inputJSON, &input) != nil || len(input.GenerationOptions) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), input.GenerationOptions...)
}

func marshalLayerMetadata(value layerMetadataV2) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func marshalFinalLayerOutput(value finalLayerOutput) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func marshalEvaluationError(value evaluationErrorJSON) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func marshalDeterministicMetadata(value deterministicLayerMetadata) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
