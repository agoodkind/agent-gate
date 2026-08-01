package hook_test

import (
	"encoding/json"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

// TestParseHookPayloadAcceptsStringToolInput exercises the entry point the
// daemon actually calls. The unit test on the decoder proves the type; this
// proves the path that rejected the call, which is what AGATE-1 reports:
// a stringified tool_input produced "parse typed hook JSON: ... cannot
// unmarshal string into Go struct field ClaudePreToolUsePayload.tool_input"
// and the tool call was refused rather than evaluated.
func TestParseHookPayloadAcceptsStringToolInput(t *testing.T) {
	const command = "grep -rn 'func ' /repo/internal/"

	inner, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal inner: %v", err)
	}

	shapes := map[string]any{
		"object":      json.RawMessage(inner),
		"json string": string(inner),
	}

	for name, toolInput := range shapes {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"session_id":      "parse-test",
				"cwd":             "/repo",
				"hook_event_name": "PreToolUse",
				"tool_name":       "Bash",
				"tool_input":      toolInput,
			})
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			payload, err := hook.ParseHookPayload(hook.SystemClaude, raw)
			if err != nil {
				t.Fatalf("ParseHookPayload rejected the %s shape: %v", name, err)
			}
			fields := payload.Fields()
			if fields.ToolInputCommand != command {
				t.Fatalf("%s shape: command = %q, want %q",
					name, fields.ToolInputCommand, command)
			}
		})
	}
}
