package hook

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestLookupCapabilityKnownPairs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		system System
		event  string
		want   Capability
	}{
		{"claude pre blocks", SystemClaude, "PreToolUse", CapabilityBlock},
		{"claude post observe", SystemClaude, "PostToolUse", CapabilityObserve},
		{"copilot pre blocks", SystemCopilot, "PreToolUse", CapabilityBlock},
		{"copilot native pre blocks", SystemCopilot, "preToolUse", CapabilityBlock},
		{"copilot post observe", SystemCopilot, "PostToolUse", CapabilityObserve},
		{"codex pre blocks", SystemCodex, "PreToolUse", CapabilityBlock},
		{"codex post substitutes", SystemCodex, "PostToolUse", CapabilitySubstitute},
		{"cursor pre blocks", SystemCursor, "preToolUse", CapabilityBlock},
		{"cursor post substitutes", SystemCursor, "postToolUse", CapabilitySubstitute},
		{"cursor after observe", SystemCursor, "afterShellExecution", CapabilityObserve},
		{"gemini before blocks", SystemGemini, "BeforeTool", CapabilityBlock},
		{"gemini after observe", SystemGemini, "AfterTool", CapabilityObserve},
	}
	for _, tc := range cases {
		got := LookupCapability(tc.system, tc.event)
		if got != tc.want {
			t.Errorf("%s: LookupCapability(%v, %q) = %s, want %s", tc.name, tc.system, tc.event, got, tc.want)
		}
	}
}

func TestLookupCapabilityUnknownDefaultsToObserve(t *testing.T) {
	t.Parallel()
	got := LookupCapability(SystemClaude, "NoSuchEvent")
	if got != CapabilityObserve {
		t.Fatalf("unknown event: got %s, want %s", got, CapabilityObserve)
	}
	got = LookupCapability(SystemUnknown, "PreToolUse")
	if got != CapabilityObserve {
		t.Fatalf("unknown system: got %s, want %s", got, CapabilityObserve)
	}
}

func TestCapabilityString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cap  Capability
		want string
	}{
		{CapabilityBlock, "block"},
		{CapabilitySubstitute, "substitute"},
		{CapabilityObserve, "observe"},
	}
	for _, tc := range cases {
		if got := tc.cap.String(); got != tc.want {
			t.Errorf("Capability(%d).String() = %q, want %q", tc.cap, got, tc.want)
		}
	}
}

func TestLookupResponseCapabilityKnownPairs(t *testing.T) {
	tests := []struct {
		name   string
		system System
		event  string
		want   ResponseCapability
	}{
		{"Claude lifecycle context", SystemClaude, "SessionStart", ResponseCapabilityInject},
		{"Claude setup context", SystemClaude, "Setup", ResponseCapabilityInject},
		{"Claude subagent start context", SystemClaude, "SubagentStart", ResponseCapabilityInject},
		{"Claude prompt context", SystemClaude, "UserPromptSubmit", ResponseCapabilityInject},
		{"Claude expanded prompt context", SystemClaude, "UserPromptExpansion", ResponseCapabilityInject},
		{"Claude stop context", SystemClaude, "Stop", ResponseCapabilityInject},
		{"Claude subagent stop context", SystemClaude, "SubagentStop", ResponseCapabilityInject},
		{"Claude tool input", SystemClaude, "PreToolUse", ResponseCapabilityInject | ResponseCapabilityToolInputMutation},
		{"Claude tool output", SystemClaude, "PostToolUse", ResponseCapabilityInject | ResponseCapabilityToolOutputMutation},
		{"Claude failed tool context", SystemClaude, "PostToolUseFailure", ResponseCapabilityInject},
		{"Claude tool batch context", SystemClaude, "PostToolBatch", ResponseCapabilityInject},
		{"Codex session context", SystemCodex, "SessionStart", ResponseCapabilityInject},
		{"Codex subagent context", SystemCodex, "SubagentStart", ResponseCapabilityInject},
		{"Codex prompt context", SystemCodex, "UserPromptSubmit", ResponseCapabilityInject},
		{"Codex tool input", SystemCodex, "PreToolUse", ResponseCapabilityInject | ResponseCapabilityToolInputMutation},
		{"Codex tool output context", SystemCodex, "PostToolUse", ResponseCapabilityInject},
		{"Cursor session context", SystemCursor, "sessionStart", ResponseCapabilityInject},
		{"Cursor stop follow-up", SystemCursor, "stop", ResponseCapabilityInject},
		{"Cursor context and MCP output", SystemCursor, "postToolUse", ResponseCapabilityInject | ResponseCapabilityToolOutputMutation},
		{"Copilot session context", SystemCopilot, "sessionStart", ResponseCapabilityInject},
		{"Copilot subagent context", SystemCopilot, "subagentStart", ResponseCapabilityInject},
		{"Copilot transformed prompt", SystemCopilot, "userPromptTransformed", ResponseCapabilityInject | ResponseCapabilityPromptMutation},
		{"Copilot tool input", SystemCopilot, "preToolUse", ResponseCapabilityToolInputMutation},
		{"Copilot tool output", SystemCopilot, "postToolUse", ResponseCapabilityInject | ResponseCapabilityToolOutputMutation},
		{"Copilot failed tool output", SystemCopilot, "postToolUseFailure", ResponseCapabilityInject},
		{"Copilot notification context", SystemCopilot, "notification", ResponseCapabilityInject},
	}
	for _, testCase := range tests {
		if got := LookupResponseCapability(testCase.system, testCase.event); got != testCase.want {
			t.Fatalf("%s: got %d, want %d", testCase.name, got, testCase.want)
		}
	}
	if got := LookupResponseCapability(SystemCursor, "beforeSubmitPrompt"); got != ResponseCapabilityNone {
		t.Fatalf("Cursor beforeSubmitPrompt capability = %d, want none", got)
	}
}

func TestWarnCapabilityDowngradesReportsCursorPromptResponseNoOp(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Rules: []config.Rule{{
		Name: "prompt-context", CursorEvents: []string{"beforeSubmitPrompt"},
		Action: config.ActionInject, Output: "context",
	}}}
	downgrades := WarnCapabilityDowngrades(context.Background(), logger, cfg)
	if len(downgrades) != 1 {
		t.Fatalf("downgrades = %#v", downgrades)
	}
	if downgrades[0].Effect != "noop" {
		t.Fatalf("effect = %q, want noop", downgrades[0].Effect)
	}
}
