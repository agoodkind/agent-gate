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

func TestNormalizePayload_DispatchesByProvider(t *testing.T) {
	raw := hook.RawPayload{
		"tool_input": map[string]any{
			"newString": "new text",
		},
	}

	claudePayload := hook.NormalizePayload(hook.SystemClaude, raw)
	claudeToolInput, ok := claudePayload["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("claude tool_input missing: %#v", claudePayload["tool_input"])
	}
	if _, ok := claudeToolInput["new_string"]; ok {
		t.Fatalf("generic payload was normalized as VS Code: %#v", claudeToolInput)
	}

	vscodePayload := hook.NormalizePayload(hook.SystemVSCode, raw)
	vscodeToolInput, ok := vscodePayload["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("vscode tool_input missing: %#v", vscodePayload["tool_input"])
	}
	if vscodeToolInput["new_string"] != "new text" {
		t.Fatalf("vscode new_string = %#v, want new text", vscodeToolInput["new_string"])
	}
}

func TestNormalizeVSCodePayload_EditToolInput(t *testing.T) {
	raw := hook.RawPayload{
		"hook_event_name": "PreToolUse",
		"tool_name":       "replace_string_in_file",
		"tool_input": map[string]any{
			"filePath":  "/project/page.zig",
			"oldString": "old text",
			"newString": "new text",
		},
	}

	got := hook.NormalizeVSCodePayload(raw)
	ti, ok := got["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_input missing: %#v", got["tool_input"])
	}
	if ti["file_path"] != "/project/page.zig" {
		t.Fatalf("tool_input.file_path = %#v, want /project/page.zig", ti["file_path"])
	}
	if ti["old_string"] != "old text" {
		t.Fatalf("tool_input.old_string = %#v, want old text", ti["old_string"])
	}
	if ti["new_string"] != "new text" {
		t.Fatalf("tool_input.new_string = %#v, want new text", ti["new_string"])
	}
}

func TestNormalizeVSCodePayload_MultiReplaceToolInput(t *testing.T) {
	raw := hook.RawPayload{
		"hook_event_name": "PreToolUse",
		"tool_name":       "multi_replace_string_in_file",
		"tool_input": map[string]any{
			"replacements": []any{
				map[string]any{
					"filePath":  "/project/page.zig",
					"oldString": "first old",
					"newString": "first new",
				},
				map[string]any{
					"filePath":  "/project/list.zig",
					"oldString": "second old",
					"newString": "second new",
				},
			},
		},
	}

	got := hook.NormalizeVSCodePayload(raw)
	ti, ok := got["tool_input"].(map[string]any)
	if !ok {
		t.Fatalf("tool_input missing: %#v", got["tool_input"])
	}
	if ti["new_string"] != "first new\nsecond new" {
		t.Fatalf("tool_input.new_string = %#v, want joined new strings", ti["new_string"])
	}
	edits, ok := got["edits"].([]any)
	if !ok || len(edits) != 2 {
		t.Fatalf("edits = %#v, want two normalized edits", got["edits"])
	}
	secondEdit, ok := edits[1].(map[string]any)
	if !ok {
		t.Fatalf("second edit has unexpected shape: %#v", edits[1])
	}
	if secondEdit["new_string"] != "second new" {
		t.Fatalf("second edit new_string = %#v, want second new", secondEdit["new_string"])
	}
}

func TestNormalizeCopilotPayload_AddsAssistantMessageFromTranscript(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "copilot.jsonl")
	dash := string(rune(0x2014))
	lines := strings.Join([]string{
		`{"type":"assistant.message","data":{"content":"First response."}}`,
		`{"type":"assistant.message","data":{"content":"Final response with em dash ` + dash + ` blocked."}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	raw := hook.RawPayload{
		"hook_event_name": "Stop",
		"transcript_path": transcript,
	}

	got := hook.NormalizeCopilotPayload(raw)
	want := "Final response with em dash " + dash + " blocked."
	if got["last_assistant_message"] != want {
		t.Fatalf("last_assistant_message = %#v, want %#v", got["last_assistant_message"], want)
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
