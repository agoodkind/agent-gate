package hook_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestCanBlock_NewProviders(t *testing.T) {
	tests := []struct {
		system hook.HookSystem
		event  string
		want   bool
	}{
		{hook.SystemCodex, "PreToolUse", true},
		{hook.SystemCodex, "SessionStart", false},
		{hook.SystemGemini, "BeforeTool", true},
		{hook.SystemGemini, "BeforeToolSelection", false},
		{hook.SystemGemini, "Notification", false},
	}

	for _, tc := range tests {
		if got := hook.CanBlock(tc.system, tc.event); got != tc.want {
			t.Fatalf("CanBlock(%q, %q) = %v, want %v", tc.system.String(), tc.event, got, tc.want)
		}
	}
}

func TestEnrichPayload_UsesToolInputWorkdir(t *testing.T) {
	raw := hook.RawPayload{
		"cwd": "/chat",
		"tool_input": map[string]any{
			"command": "go test ./...",
			"workdir": "/project",
		},
	}

	got := hook.EnrichPayload(raw)
	if got["cwd"] != "/chat" {
		t.Fatalf("cwd was overwritten: %#v", got)
	}
	if got["effective_cwd"] != "/project" {
		t.Fatalf("effective_cwd = %#v, want /project", got["effective_cwd"])
	}
}

func TestEnrichPayload_UsesTranscriptFunctionCallWorkdir(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"go test ./...\",\"workdir\":\"/project\"}","call_id":"call_123"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	raw := hook.RawPayload{
		"cwd":             "/chat",
		"transcript_path": transcript,
		"tool_use_id":     "call_123",
		"tool_input":      map[string]any{"command": "go test ./..."},
	}

	got := hook.EnrichPayload(raw)
	if got["cwd"] != "/chat" {
		t.Fatalf("cwd was overwritten: %#v", got)
	}
	if got["effective_cwd"] != "/project" {
		t.Fatalf("effective_cwd = %#v, want /project", got["effective_cwd"])
	}
	ti, ok := got["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_input missing: %#v", got["tool_input"])
	}
	if ti["workdir"] != "/project" {
		t.Fatalf("tool_input.workdir = %#v, want /project", ti["workdir"])
	}
}

func TestEnrichPayload_TranscriptNonMatchLeavesPayloadAlone(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "rollout.jsonl")
	line := `{"type":"response_item","payload":{"type":"function_call","arguments":"{\"workdir\":\"/project\"}","call_id":"other"}}` + "\n"
	if err := os.WriteFile(transcript, []byte(line), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	raw := hook.RawPayload{
		"cwd":             "/chat",
		"transcript_path": transcript,
		"tool_use_id":     "call_123",
	}

	got := hook.EnrichPayload(raw)
	if _, ok := got["effective_cwd"]; ok {
		t.Fatalf("unexpected effective_cwd: %#v", got)
	}
}

func TestCodexBlockResponses(t *testing.T) {
	got := string(hook.CodexBlock("PreToolUse", "r", "blocked"))
	if !strings.Contains(got, `"permissionDecision":"deny"`) {
		t.Fatalf("CodexBlock(PreToolUse) missing deny payload: %s", got)
	}

	got = string(hook.CodexBlock("PermissionRequest", "r", "blocked"))
	if !strings.Contains(got, `"behavior":"deny"`) {
		t.Fatalf("CodexBlock(PermissionRequest) missing deny behavior: %s", got)
	}

	got = string(hook.CodexBlock("Stop", "r", "blocked"))
	if !strings.Contains(got, `"decision":"block"`) {
		t.Fatalf("CodexBlock(Stop) missing block decision: %s", got)
	}
}

func TestBlockTextResponses(t *testing.T) {
	diagnostic := "agent-gate blocked 2 violations:\n\nassistant_message\n1 | alpha xx\n  |       ^A"
	if got := string(hook.CodexBlockText("Stop", diagnostic)); !strings.Contains(got, "agent-gate blocked 2 violations") || !strings.Contains(got, "alpha xx") {
		t.Fatalf("CodexBlockText missing diagnostic: %s", got)
	}
	if got := string(hook.CursorBlockText(diagnostic)); !strings.Contains(got, `"permission":"deny"`) || !strings.Contains(got, "alpha xx") {
		t.Fatalf("CursorBlockText missing deny diagnostic: %s", got)
	}
	if got := string(hook.ClaudeBlockText(diagnostic)); got != diagnostic+"\n" {
		t.Fatalf("ClaudeBlockText = %q", got)
	}
	if got := string(hook.GeminiBlockText("BeforeTool", diagnostic)); !strings.Contains(got, `"decision":"deny"`) || !strings.Contains(got, "alpha xx") {
		t.Fatalf("GeminiBlockText missing deny diagnostic: %s", got)
	}
}

func TestGeminiBlockResponses(t *testing.T) {
	got := string(hook.GeminiBlock("BeforeTool", "r", "blocked"))
	if !strings.Contains(got, `"decision":"deny"`) || !strings.Contains(got, `"reason":"agent-gate: [r] blocked"`) {
		t.Fatalf("GeminiBlock(BeforeTool) missing deny response: %s", got)
	}
}

func TestValidPaths_NewProviders(t *testing.T) {
	if got := hook.ValidPaths("codex", "PreToolUse"); !got["tool_input.command"] || !got["turn_id"] {
		t.Fatalf("codex PreToolUse schema missing expected paths: %#v", got)
	}
	if got := hook.ValidPaths("gemini", "BeforeTool"); !got["tool_name"] || !got["tool_input.command"] {
		t.Fatalf("gemini BeforeTool schema missing expected paths: %#v", got)
	}
}

func TestValidateConfig_NewProviderSpecificEvents(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{
			{
				Name:        "codex-tool-command",
				CodexEvents: []string{"PreToolUse"},
				FieldPaths:  []string{"tool_input.command"},
			},
			{
				Name:         "gemini-tool-command",
				GeminiEvents: []string{"BeforeTool"},
				FieldPaths:   []string{"tool_input.command"},
			},
		},
	}

	if errs := hook.ValidateConfig(cfg); len(errs) != 0 {
		t.Fatalf("ValidateConfig returned unexpected errors: %v", errs)
	}
}

func TestValidateConfig_ConditionKinds(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{
			{
				Name:        "known-kinds",
				CodexEvents: []string{"PreToolUse"},
				Conditions: []config.Condition{
					{Kind: "command", Argv0: "go"},
					{Kind: "command", Argv0: "go", StripEnv: true, StripArgs: []string{"env"}, CwdFlags: []string{"-C"}},
					{Kind: "project", RootMarkers: []string{"go.mod"}},
					{Kind: "regex", FieldPaths: []string{"tool_input.command"}, Pattern: "x"},
				},
			},
		},
	}
	if errs := hook.ValidateConfig(cfg); len(errs) != 0 {
		t.Fatalf("ValidateConfig returned unexpected errors: %v", errs)
	}

	cfg.Rules[0].Conditions = append(cfg.Rules[0].Conditions, config.Condition{Kind: "unknown"})
	if errs := hook.ValidateConfig(cfg); len(errs) == 0 {
		t.Fatal("expected unknown condition kind error")
	}
}
