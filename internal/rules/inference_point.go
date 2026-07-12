package rules

import (
	"context"
	"encoding/json"
	"time"

	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
)

// defaultPointTimeout bounds a single inference-point call when the point does
// not set its own timeout.
const defaultPointTimeout = 4 * time.Second

// decisionOutputSchema is the JSON Schema an inference point answers with when it
// judges a command against a rule: a single decision of allow or block.
const decisionOutputSchema = `{"type":"object","properties":{"decision":{"type":"string","enum":["allow","block"]}},"required":["decision"],"additionalProperties":false}`

// pointVerdict is the outcome of evaluating one inference point, plus the fields
// needed to record it as an inference layer in the decision trace. upstream holds
// the model's invocation metadata, whose logprob-derived confidence is recorded
// for a backend that reports it (mini) and absent for one that does not (v4).
type pointVerdict struct {
	block       bool
	errored     bool
	outputJSON  string
	errorCode   string
	model       string
	upstream    UpstreamMetadata
	startedAt   time.Time
	completedAt time.Time
}

// evaluatePoint asks one inference point to judge input against prompt, returning
// its decision plus the model's invocation metadata. A transport, status, or parse
// failure returns errored so the caller can apply the entry's on-error policy.
func (runtime *InferRuntime) evaluatePoint(ctx context.Context, point config.InferencePoint, prompt, input string) pointVerdict {
	startedAt := runtime.now()
	fail := func(code string) pointVerdict {
		return pointVerdict{
			block: false, errored: true,
			outputJSON: "{}", errorCode: code, model: point.Model,
			upstream:  boundedUpstreamMetadata(nil),
			startedAt: startedAt, completedAt: runtime.now(),
		}
	}
	timeout := time.Duration(point.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultPointTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := runtime.inferenceClient(point.Endpoint)
	if err != nil {
		return fail("invalid_endpoint")
	}
	reply, err := client.Infer(callCtx, &inferencepb.InferRequest{
		Prompt:            prompt,
		Input:             input,
		OutputSchema:      decisionOutputSchema,
		Context:           "",
		Model:             point.Model,
		GenerationOptions: pointGenerationOptions(point),
	})
	if err != nil {
		return fail(grpcErrorClass(err))
	}
	if reply == nil || reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
		return fail("non_complete")
	}
	decision, ok := parsePointDecision(reply.GetOutputJson())
	if !ok {
		return fail("invalid_response")
	}
	return pointVerdict{
		block:       decision == "block",
		errored:     false,
		outputJSON:  reply.GetOutputJson(),
		errorCode:   "",
		model:       point.Model,
		upstream:    boundedUpstreamMetadata(reply.GetMetadata()),
		startedAt:   startedAt,
		completedAt: runtime.now(),
	}
}

// recordPointLayer records one inference-point call as an inference layer in the
// decision trace, so every routed verdict appears in gate_evaluation_layers.
// traceIndex separates layers for the same rule, and the model's invocation
// metadata (including its confidence when reported) rides in the layer's upstream
// metadata.
func recordPointLayer(ctx context.Context, ruleName string, traceIndex int, verdict pointVerdict) {
	collector := richTraceCollectorFromContext(ctx)
	if collector == nil {
		return
	}
	status := traceStatusComplete
	outcome := "nonmatch"
	if verdict.block {
		outcome = "match"
	}
	if verdict.errored {
		status = traceStatusError
		outcome = ""
	}
	output := json.RawMessage(verdict.outputJSON)
	if len(output) == 0 {
		output = json.RawMessage(`{}`)
	}
	layer := newLayerTrace(ruleName, traceIndex, verdict.model, "inference")
	layer.Status = status
	layer.Outcome = outcome
	layer.StartedAt = verdict.startedAt
	layer.CompletedAt = verdict.completedAt
	layer.OutputJSON = output
	layer.OutputHash = traceJSONHash(output)
	layer.ServiceName = "inference"
	layer.ErrorCode = verdict.errorCode
	layer.UpstreamMetadata = verdict.upstream
	layer.UpstreamMetadata.Raw = append(json.RawMessage(nil), verdict.upstream.Raw...)
	collector.collect(layer)
}

func pointGenerationOptions(point config.InferencePoint) *inferencepb.GenerationOptions {
	if point.ReasoningEffort == "" {
		return nil
	}
	return &inferencepb.GenerationOptions{
		ReasoningEffort:     reasoningEffortValue(config.ReasoningEffort(point.ReasoningEffort)),
		MaxCompletionTokens: nil,
		Temperature:         nil,
	}
}

func parsePointDecision(outputJSON string) (string, bool) {
	var decoded struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(outputJSON), &decoded); err != nil {
		return "", false
	}
	if decoded.Decision != "allow" && decoded.Decision != "block" {
		return "", false
	}
	return decoded.Decision, true
}
