package hook_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

var allTrackedEnvVars = []string{
	"CODEX_THREAD_ID",
	"CODEX_CI",
	"COPILOT_OTEL_FILE_EXPORTER_PATH",
	"COPILOT_OTEL_ENABLED",
	"COPILOT_OTEL_EXPORTER_TYPE",
	"CURSOR_VERSION",
	"CURSOR_WORKSPACE_NAME",
	"CURSOR_MODE",
	"GEMINI_CLI",
	"CLAUDE_CODE_ENTRYPOINT",
	"AI_AGENT",
	"VSCODE_PID",
	"VSCODE_IPC_HOOK",
	"TERM_PROGRAM",
}

func clearTrackedEnv(t *testing.T) {
	t.Helper()
	for _, variable := range allTrackedEnvVars {
		t.Setenv(variable, "")
	}
}

func TestDetect_PriorityChain(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		payload hook.DetectionPayload
		hint    hook.HookSystem
		want    hook.HookSystem
	}{
		{name: "codex env beats claude env", env: map[string]string{"CODEX_THREAD_ID": "abc", "CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemCodex},
		{name: "codex CI flag alone", env: map[string]string{"CODEX_CI": "1"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemCodex},
		{name: "cursor env beats claude payload", env: map[string]string{"CURSOR_VERSION": "0.42"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse", PermissionMode: "default"}, want: hook.SystemCursor},
		{name: "cursor payload markers", payload: hook.DetectionPayload{HookEventName: "PreToolUse", CursorVersion: "0.42"}, want: hook.SystemCursor},
		{name: "cursor camel case event", payload: hook.DetectionPayload{HookEventName: "beforeShellExecution"}, want: hook.SystemCursor},
		{name: "gemini env", env: map[string]string{"GEMINI_CLI": "1", "CLAUDECODE": "1"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemGemini},
		{name: "gemini event name", payload: hook.DetectionPayload{HookEventName: "BeforeTool"}, want: hook.SystemGemini},
		{name: "claude env CLAUDE_CODE_ENTRYPOINT", env: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "claude env AI_AGENT", env: map[string]string{"AI_AGENT": "claude-code/2.1.121/agent"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "claude payload transcript_path", payload: hook.DetectionPayload{HookEventName: "PreToolUse", TranscriptPath: "/tmp/x.jsonl"}, want: hook.SystemClaude},
		{name: "claude payload permission_mode", payload: hook.DetectionPayload{HookEventName: "Stop", PermissionMode: "default"}, want: hook.SystemClaude},
		{name: "copilot env beats claude payload", env: map[string]string{"COPILOT_OTEL_FILE_EXPORTER_PATH": "/dev/null", "VSCODE_PID": "62178"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse", TranscriptPath: "/tmp/x.jsonl"}, want: hook.SystemCopilot},
		{name: "copilot OTEL_ENABLED alone", env: map[string]string{"COPILOT_OTEL_ENABLED": "true"}, payload: hook.DetectionPayload{HookEventName: "UserPromptSubmit"}, want: hook.SystemCopilot},
		{name: "vscode env without other markers", env: map[string]string{"VSCODE_PID": "12345"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemVSCode},
		{name: "vscode env loses to claude env", env: map[string]string{"VSCODE_PID": "12345", "CLAUDE_CODE_ENTRYPOINT": "cli"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemClaude},
		{name: "subcommand hint reached when nothing else matches", payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, hint: hook.SystemCodex, want: hook.SystemCodex},
		{name: "subcommand hint outranked by cursor payload", payload: hook.DetectionPayload{HookEventName: "PreToolUse", CursorVersion: "0.42"}, hint: hook.SystemCodex, want: hook.SystemCursor},
		{name: "pure pascal case with no markers returns unknown", payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
		{name: "empty event name returns unknown", payload: hook.DetectionPayload{}, want: hook.SystemUnknown},
		{name: "term_program ghostty does not trigger vscode", env: map[string]string{"TERM_PROGRAM": "ghostty"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
		{name: "term_program vscode alone is not a vscode signal", env: map[string]string{"TERM_PROGRAM": "vscode"}, payload: hook.DetectionPayload{HookEventName: "PreToolUse"}, want: hook.SystemUnknown},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			clearTrackedEnv(t)
			for key, value := range testCase.env {
				t.Setenv(key, value)
			}
			got := hook.Detect(testCase.payload, testCase.hint)
			if got != testCase.want {
				t.Errorf("Detect() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestHookPayloadAccessors(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemClaude, []byte(`{"hook_event_name":"PreToolUse","session_id":"abc123","cwd":"/tmp/project"}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.EventName(); got != "PreToolUse" {
		t.Errorf("EventName() = %q, want %q", got, "PreToolUse")
	}
	if got := payload.SessionID(); got != "abc123" {
		t.Errorf("SessionID() = %q, want %q", got, "abc123")
	}
	if got := payload.CWD(); got != "/tmp/project" {
		t.Errorf("CWD() = %q, want %q", got, "/tmp/project")
	}
}

func TestUnknownPayloadSessionIDFallsBackToConversationID(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemUnknown, []byte(`{"conversation_id":"cursor-conv-999"}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.SessionID(); got != "cursor-conv-999" {
		t.Errorf("SessionID() fallback = %q, want %q", got, "cursor-conv-999")
	}
}
