package rules

import (
	"context"
	"encoding/json"
	"time"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
)

type inferenceLayerInput struct {
	Prompt            string                         `json:"prompt"`
	Input             string                         `json:"input"`
	OutputSchema      json.RawMessage                `json:"output_schema"`
	Context           json.RawMessage                `json:"context"`
	ConfiguredModel   string                         `json:"configured_model"`
	GenerationOptions *inferencepb.GenerationOptions `json:"generation_options,omitempty"`
	ResponseField     string                         `json:"response_json_field"`
	ResponseEquals    string                         `json:"response_json_equals"`
	BlockOn           string                         `json:"block_on"`
}

type contextLayerInput struct {
	Workspace       string `json:"workspace"`
	Session         string `json:"session"`
	TurnBudget      int    `json:"turn_budget"`
	MaxCharsPerTurn int    `json:"max_chars_per_turn"`
	Endpoint        string `json:"endpoint"`
}

type traceErrorCode string

const (
	traceErrorNone               traceErrorCode = ""
	traceErrorHashMismatch       traceErrorCode = "hash_mismatch"
	traceErrorContextUnavailable traceErrorCode = "context_unavailable"
	traceErrorContextInvalid     traceErrorCode = "context_invalid"
	traceErrorInvalidResponse    traceErrorCode = "invalid_response"
	traceErrorNonComplete        traceErrorCode = "non_complete"
	traceErrorInvalidEndpoint    traceErrorCode = "invalid_endpoint"
)

type richTraceCall struct {
	rule             *config.Rule
	condition        *config.Condition
	conditionIndex   int
	input            string
	contextWorkspace string
	contextSession   string
	cacheKey         string
	startedAt        time.Time
	completedAt      time.Time
	result           inferResult
}

func (runtime *InferRuntime) collectRichTrace(
	ctx context.Context,
	rule *config.Rule,
	condition *config.Condition,
	conditionIndex int,
	input string,
	contextWorkspace string,
	contextSession string,
	cacheKey string,
	startedAt time.Time,
	completedAt time.Time,
	result inferResult,
) {
	collector := richTraceCollectorFromContext(ctx)
	if collector == nil {
		return
	}
	call := richTraceCall{
		rule: rule, condition: condition, conditionIndex: conditionIndex,
		input: input, contextWorkspace: contextWorkspace, contextSession: contextSession,
		cacheKey: cacheKey, startedAt: startedAt, completedAt: completedAt, result: result,
	}
	parentIndex := 0
	if condition.ContextSource != "" {
		parentIndex = collectRichContextTrace(collector, call)
	}
	collectRichInferenceTrace(collector, call, parentIndex)
}

func collectRichContextTrace(collector *richTraceCollector, call richTraceCall) int {
	condition := call.condition
	result := call.result
	contextInput := marshalContextTraceJSON(contextLayerInput{
		Workspace: call.contextWorkspace, Session: call.contextSession,
		TurnBudget:      condition.ContextTurnBudget,
		MaxCharsPerTurn: condition.ContextMaxCharsPerTurn,
		Endpoint:        condition.ContextEndpoint,
	})
	contextStatus := traceStatusComplete
	contextSkipReason := ""
	contextErrorCode := result.contextErrorClass
	contextErrorMessage := sanitizedTraceError(result.contextErrorClass)
	contextOutput := append(json.RawMessage(nil), result.contextJSON...)
	contextStartedAt := result.contextStartedAt
	contextCompletedAt := result.contextCompletedAt
	if result.cacheStatus == "hit" {
		contextStatus = traceStatusSkipped
		contextSkipReason = "cache_hit"
		contextErrorCode = ""
		contextErrorMessage = ""
		contextOutput = json.RawMessage(`{}`)
		contextStartedAt = call.startedAt
		contextCompletedAt = call.startedAt
	} else if result.contextErrorClass != "" {
		contextStatus = traceStatusError
		contextOutput = json.RawMessage(`{}`)
	}
	contextLayer := newLayerTrace(
		call.rule.Name,
		call.conditionIndex,
		call.rule.Name+"/"+condition.LayerName+"/context",
		"context",
	)
	parentIndex := 0
	contextLayer.Status = contextStatus
	contextLayer.SkipReason = contextSkipReason
	contextLayer.ParentTraceIndex = &parentIndex
	contextLayer.StartedAt = contextStartedAt
	contextLayer.CompletedAt = contextCompletedAt
	contextLayer.InputReference = "intake.context_identity"
	contextLayer.InputJSON = contextInput
	contextLayer.OutputJSON = contextOutput
	contextLayer.InputHash = traceJSONHash(contextInput)
	contextLayer.OutputHash = traceJSONHash(contextOutput)
	contextLayer.ServiceName = "clyde"
	contextLayer.ErrorCode = contextErrorCode
	contextLayer.ErrorMessage = contextErrorMessage
	collector.collect(contextLayer)
	return collector.traceIndex(call.rule.Name, call.conditionIndex, "context")
}

func collectRichInferenceTrace(
	collector *richTraceCollector,
	call richTraceCall,
	parentIndex int,
) {
	condition := call.condition
	result := call.result
	contextJSON := append(json.RawMessage(nil), result.contextJSON...)
	if len(contextJSON) == 0 {
		contextJSON = json.RawMessage(`{}`)
	}
	inputJSON := marshalInferenceTraceJSON(inferenceLayerInput{
		Prompt: condition.Prompt, Input: call.input,
		OutputSchema: json.RawMessage(condition.OutputSchema), Context: contextJSON,
		ConfiguredModel: condition.Model, GenerationOptions: generationOptions(condition),
		ResponseField:  condition.ResponseJSONField,
		ResponseEquals: condition.ResponseJSONEqualsValue().CanonicalString(),
		BlockOn:        condition.BlockOn,
	})
	outputJSON := append(json.RawMessage(nil), result.outputJSON...)
	if len(outputJSON) == 0 {
		outputJSON = json.RawMessage(`{}`)
	}
	inferenceStartedAt := call.startedAt
	if condition.ContextSource != "" && !result.contextCompletedAt.IsZero() {
		inferenceStartedAt = result.contextCompletedAt
	}
	statusValue := traceStatusComplete
	outcome := "nonmatch"
	if result.matched {
		outcome = "match"
	}
	if result.errored {
		statusValue = traceStatusError
		outcome = ""
	}
	layer := newLayerTrace(call.rule.Name, call.conditionIndex, condition.LayerName, "inference")
	layer.Status = statusValue
	layer.Outcome = outcome
	layer.ParentTraceIndex = &parentIndex
	layer.StartedAt = inferenceStartedAt
	layer.CompletedAt = call.completedAt
	layer.InputReference = "intake.normalized_json"
	layer.InputJSON = inputJSON
	layer.OutputJSON = outputJSON
	layer.InputHash = traceJSONHash(inputJSON)
	layer.OutputHash = traceJSONHash(outputJSON)
	layer.ServiceName = "inference"
	layer.VerifiedProvenance = verifiedInferenceProvenance(
		condition, call.input, call.cacheKey, result,
	)
	layer.CacheStatus = result.cacheStatus
	layer.CacheKeyHash = layer.VerifiedProvenance.CacheKeyHash
	layer.CacheEntryVersion = result.cacheEntryVersion
	layer.CacheExpiresAt = result.cacheExpiresAt
	layer.UpstreamMetadata = result.upstreamMetadata
	layer.UpstreamMetadata.Raw = append(
		json.RawMessage(nil), result.upstreamMetadata.Raw...,
	)
	layer.ErrorCode = result.errorClass
	layer.ErrorMessage = sanitizedTraceError(result.errorClass)
	collector.collect(layer)
}

func marshalContextTraceJSON(value contextLayerInput) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func marshalInferenceTraceJSON(value inferenceLayerInput) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

func sanitizedTraceError(errorClass string) string {
	switch traceErrorCode(errorClass) {
	case traceErrorNone:
		return ""
	case traceErrorHashMismatch:
		return "inference provenance hash mismatch"
	case traceErrorContextUnavailable:
		return "context service unavailable"
	case traceErrorContextInvalid:
		return "context response invalid"
	case traceErrorInvalidResponse:
		return "inference response invalid"
	case traceErrorNonComplete:
		return "inference response incomplete"
	case traceErrorInvalidEndpoint:
		return "inference endpoint invalid"
	default:
		return "inference request failed"
	}
}
