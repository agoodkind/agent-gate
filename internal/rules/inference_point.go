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

// pointVerdict is the outcome of evaluating one inference point.
type pointVerdict struct {
	block             bool
	confidence        float64
	confidencePresent bool
	errored           bool
}

// evaluatePoint asks one inference point to judge input against prompt, returning
// its decision plus any logprob-derived confidence. A transport, status, or parse
// failure returns errored so the caller can fail closed.
func (runtime *InferRuntime) evaluatePoint(ctx context.Context, point config.InferencePoint, prompt, input string) pointVerdict {
	failed := pointVerdict{block: false, confidence: 0, confidencePresent: false, errored: true}
	timeout := time.Duration(point.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultPointTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := runtime.inferenceClient(point.Endpoint)
	if err != nil {
		return failed
	}
	reply, err := client.Infer(callCtx, &inferencepb.InferRequest{
		Prompt:            prompt,
		Input:             input,
		OutputSchema:      decisionOutputSchema,
		Context:           "",
		Model:             point.Model,
		GenerationOptions: pointGenerationOptions(point),
	})
	if err != nil || reply == nil || reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
		return failed
	}
	decision, ok := parsePointDecision(reply.GetOutputJson())
	if !ok {
		return failed
	}
	confidence, confidencePresent := pointConfidence(reply.GetMetadata())
	return pointVerdict{
		block:             decision == "block",
		confidence:        confidence,
		confidencePresent: confidencePresent,
		errored:           false,
	}
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

func pointConfidence(metadata *inferencepb.InvocationMetadata) (float64, bool) {
	if metadata == nil || metadata.Confidence == nil {
		return 0, false
	}
	return metadata.GetConfidence(), true
}
