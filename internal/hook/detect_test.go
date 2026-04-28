package hook_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

// allTrackedEnvVars is the set of every env var Detect inspects. Each
// table-driven case clears all of them via t.Setenv and then sets only
// the ones the case needs. This avoids leaking state from the host
// environment (which itself is often a claude or codex shell).
var allTrackedEnvVars = []string{
	"CODEX_THREAD_ID",
	"CODEX_CI",
	"CURSOR_VERSION",
	"CURSOR_WORKSPACE_NAME",
	"CURSOR_MODE",
	"GEMINI_CLI",
	"CLAUDE_CODE_ENTRYPOINT",
	"AI_AGENT",
	"TERM_PROGRAM",
}

func clearTrackedEnv(t *testing.T) {
	t.Helper()
	for _, v := range allTrackedEnvVars {
		t.Setenv(v, "")
	}
}

func TestDetect_PriorityChain(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		payload hook.RawPayload
		hint    hook.HookSystem
		want    hook.HookSystem
	}{
		{
			name:    "codex env beats claude env (nested invocation)",
			env:     map[string]string{"CODEX_THREAD_ID": "abc", "CLAUDE_CODE_ENTRYPOINT": "cli"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemCodex,
		},
		{
			name:    "codex CI flag alone",
			env:     map[string]string{"CODEX_CI": "1"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemCodex,
		},
		{
			name:    "cursor env beats claude payload",
			env:     map[string]string{"CURSOR_VERSION": "0.42"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse", "permission_mode": "default"},
			want:    hook.SystemCursor,
		},
		{
			name:    "cursor payload markers",
			payload: hook.RawPayload{"hook_event_name": "PreToolUse", "cursor_version": "0.42"},
			want:    hook.SystemCursor,
		},
		{
			name:    "cursor camel case event",
			payload: hook.RawPayload{"hook_event_name": "beforeShellExecution"},
			want:    hook.SystemCursor,
		},
		{
			name:    "gemini env",
			env:     map[string]string{"GEMINI_CLI": "1", "CLAUDECODE": "1"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemGemini,
		},
		{
			name:    "gemini event name",
			payload: hook.RawPayload{"hook_event_name": "BeforeTool"},
			want:    hook.SystemGemini,
		},
		{
			name:    "claude env CLAUDE_CODE_ENTRYPOINT",
			env:     map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemClaude,
		},
		{
			name:    "claude env AI_AGENT",
			env:     map[string]string{"AI_AGENT": "claude-code/2.1.121/agent"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemClaude,
		},
		{
			name:    "claude payload transcript_path",
			payload: hook.RawPayload{"hook_event_name": "PreToolUse", "transcript_path": "/tmp/x.jsonl"},
			want:    hook.SystemClaude,
		},
		{
			name:    "claude payload permission_mode",
			payload: hook.RawPayload{"hook_event_name": "Stop", "permission_mode": "default"},
			want:    hook.SystemClaude,
		},
		{
			name:    "vscode env without other markers",
			env:     map[string]string{"TERM_PROGRAM": "vscode"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemVSCode,
		},
		{
			name:    "vscode env loses to claude env",
			env:     map[string]string{"TERM_PROGRAM": "vscode", "CLAUDE_CODE_ENTRYPOINT": "cli"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemClaude,
		},
		{
			name:    "subcommand hint reached when nothing else matches",
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			hint:    hook.SystemCodex,
			want:    hook.SystemCodex,
		},
		{
			name:    "subcommand hint outranked by cursor payload",
			payload: hook.RawPayload{"hook_event_name": "PreToolUse", "cursor_version": "0.42"},
			hint:    hook.SystemCodex,
			want:    hook.SystemCursor,
		},
		{
			name:    "pure pascal case with no markers returns unknown",
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemUnknown,
		},
		{
			name:    "empty event name returns unknown",
			payload: hook.RawPayload{},
			want:    hook.SystemUnknown,
		},
		{
			name:    "term_program ghostty does not trigger vscode",
			env:     map[string]string{"TERM_PROGRAM": "ghostty"},
			payload: hook.RawPayload{"hook_event_name": "PreToolUse"},
			want:    hook.SystemUnknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearTrackedEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got := hook.Detect(tc.payload, tc.hint)
			if got != tc.want {
				t.Errorf("Detect() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRawPayload_Accessors(t *testing.T) {
	p := hook.RawPayload{
		"hook_event_name": "PreToolUse",
		"session_id":      "abc123",
		"cwd":             "/tmp/project",
	}

	if got := p.EventName(); got != "PreToolUse" {
		t.Errorf("EventName() = %q, want %q", got, "PreToolUse")
	}
	if got := p.SessionID(); got != "abc123" {
		t.Errorf("SessionID() = %q, want %q", got, "abc123")
	}
	if got := p.CWD(); got != "/tmp/project" {
		t.Errorf("CWD() = %q, want %q", got, "/tmp/project")
	}
}

func TestRawPayload_SessionID_FallsBackToConversationID(t *testing.T) {
	p := hook.RawPayload{
		"conversation_id": "cursor-conv-999",
	}
	if got := p.SessionID(); got != "cursor-conv-999" {
		t.Errorf("SessionID() fallback = %q, want %q", got, "cursor-conv-999")
	}
}
