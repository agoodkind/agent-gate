package hook_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestNormalizeCopilotPayloadComposesTransformedPrompt(t *testing.T) {
	rawJSON := []byte(`{"sessionId":"s1","prompt":"original prompt","transformedPrompt":"transformed prompt"}`)
	normalized, err := hook.NormalizeCopilotPayload(rawJSON, "userPromptTransformed")
	if err != nil {
		t.Fatalf("NormalizeCopilotPayload() error: %v", err)
	}
	payload, err := hook.ParseHookPayload(hook.SystemCopilot, normalized)
	if err != nil {
		t.Fatalf("ParseHookPayload() error: %v", err)
	}
	if payload.EventName() != "userPromptTransformed" {
		t.Fatalf("EventName() = %q", payload.EventName())
	}
	if payload.Fields().Prompt != "transformed prompt" {
		t.Fatalf("prompt = %q", payload.Fields().Prompt)
	}
	cfg := &config.Config{Rules: []config.Rule{{
		Name: "turn-context", CopilotEvents: []string{"userPromptTransformed"},
		Action: config.ActionInject, Output: "turn context",
	}}}
	evaluation := hook.EvaluateHot(
		context.Background(),
		normalized,
		cfg,
		hook.SystemCopilot,
		func(key string) string {
			if key == "COPILOT_OTEL_ENABLED" {
				return "true"
			}
			return ""
		},
	)
	if !strings.Contains(string(evaluation.Stdout), `"modifiedTransformedPrompt":"turn context\n\ntransformed prompt"`) {
		t.Fatalf("response = %q", evaluation.Stdout)
	}
}

func TestNormalizeCopilotPayloadNormalizesToolResult(t *testing.T) {
	rawJSON := []byte(`{"sessionId":"s1","toolName":"shell","toolResult":{"resultType":"success","textResultForLlm":"done"}}`)
	normalized, err := hook.NormalizeCopilotPayload(rawJSON, "postToolUse")
	if err != nil {
		t.Fatalf("NormalizeCopilotPayload() error: %v", err)
	}
	var normalizedPayload map[string]json.RawMessage
	if err := json.Unmarshal(normalized, &normalizedPayload); err != nil {
		t.Fatalf("Unmarshal normalized payload: %v", err)
	}
	if got := string(normalizedPayload["tool_response"]); got != `{"resultType":"success","textResultForLlm":"done"}` {
		t.Fatalf("tool_response = %s", got)
	}
}
