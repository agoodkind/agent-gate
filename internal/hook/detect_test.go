package hook_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestDetect_Claude(t *testing.T) {
	claudeEvents := []string{
		"PreToolUse",
		"PostToolUse",
		"SessionStart",
		"SessionEnd",
		"UserPromptSubmit",
		"Stop",
		"StopFailure",
		"SubagentStart",
		"SubagentStop",
		"PermissionRequest",
		"PermissionDenied",
		"Notification",
		"ConfigChange",
		"CwdChanged",
		"InstructionsLoaded",
		"PreCompact",
		"PostCompact",
		"WorktreeCreate",
		"WorktreeRemove",
		"FileChanged",
		"TaskCreated",
		"TaskCompleted",
		"Elicitation",
		"ElicitationResult",
		"TeammateIdle",
		"PostToolUseFailure",
	}

	for _, event := range claudeEvents {
		t.Run(event, func(t *testing.T) {
			p := hook.RawPayload{"hook_event_name": event}
			if got := hook.Detect(p); got != hook.SystemClaude {
				t.Errorf("Detect(%q) = %v, want SystemClaude", event, got)
			}
		})
	}
}

func TestDetect_Cursor(t *testing.T) {
	cursorEvents := []string{
		"beforeShellExecution",
		"beforeMCPExecution",
		"beforeSubmitPrompt",
		"beforeReadFile",
		"afterFileEdit",
		"stop",
	}

	for _, event := range cursorEvents {
		t.Run(event, func(t *testing.T) {
			p := hook.RawPayload{"hook_event_name": event}
			if got := hook.Detect(p); got != hook.SystemCursor {
				t.Errorf("Detect(%q) = %v, want SystemCursor", event, got)
			}
		})
	}
}

func TestDetect_Unknown(t *testing.T) {
	cases := []hook.RawPayload{
		{},
		{"hook_event_name": ""},
		{"hook_event_name": 42},
	}

	for _, p := range cases {
		if got := hook.Detect(p); got != hook.SystemUnknown {
			t.Errorf("Detect(%v) = %v, want SystemUnknown", p, got)
		}
	}
}

func TestDetectWithOverride(t *testing.T) {
	p := hook.RawPayload{"hook_event_name": "SessionStart"}

	if got := hook.DetectWithOverride(p, hook.SystemCodex); got != hook.SystemCodex {
		t.Fatalf("DetectWithOverride(..., SystemCodex) = %v, want SystemCodex", got)
	}
	if got := hook.DetectWithOverride(p, hook.SystemGemini); got != hook.SystemGemini {
		t.Fatalf("DetectWithOverride(..., SystemGemini) = %v, want SystemGemini", got)
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
