package hook_test

import (
	"os"
	"path/filepath"
	"strconv"
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

	for _, testCase := range tests {
		if got := hook.CanBlock(testCase.system, testCase.event); got != testCase.want {
			t.Fatalf("CanBlock(%q, %q) = %v, want %v", testCase.system.String(), testCase.event, got, testCase.want)
		}
	}
}

func TestParseHookPayload_UsesToolInputWorkdir(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemCodex, []byte(`{"hook_event_name":"PreToolUse","cwd":"/chat","tool_input":{"command":"go test ./...","workdir":"/project"}}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.CWD != "/chat" {
		t.Fatalf("cwd was overwritten: %#v", fields)
	}
	if fields.String(config.FieldEffectiveCWD) != "/project" {
		t.Fatalf("effective_cwd = %#v, want /project", fields.String(config.FieldEffectiveCWD))
	}
}

func TestCursorBeforeMCPExecution_ObjectToolInput(t *testing.T) {
	rawJSON := []byte(`{"hook_event_name":"beforeMCPExecution","session_id":"s1","tool_name":"mcp__docker__logs","tool_use_id":"call_1","cwd":"/repo","tool_input":{"command":"docker logs api","query":"errors"}}`)
	payload, err := hook.ParseHookPayload(hook.SystemCursor, rawJSON)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.ToolInputCommand != "docker logs api" {
		t.Fatalf("tool_input.command = %#v, want docker logs api", fields.ToolInputCommand)
	}
	if fields.ToolInputQuery != "errors" {
		t.Fatalf("tool_input.query = %#v, want errors", fields.ToolInputQuery)
	}
}

func TestCursorBeforeMCPExecution_StringToolInput(t *testing.T) {
	rawJSON := []byte(`{"hook_event_name":"beforeMCPExecution","session_id":"s1","tool_name":"mcp__docker__logs","tool_use_id":"call_1","cwd":"/repo","tool_input":"docker logs api"}`)
	payload, err := hook.ParseHookPayload(hook.SystemCursor, rawJSON)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.Fields().ToolInputContent; got != "docker logs api" {
		t.Fatalf("tool_input.content = %#v, want docker logs api", got)
	}
}

func TestCursorBeforeMCPExecution_JSONStringToolInput(t *testing.T) {
	toolInput := strconv.Quote(`{"command":"docker logs api","query":"errors"}`)
	rawJSON := []byte(`{"hook_event_name":"beforeMCPExecution","session_id":"s1","tool_name":"mcp__docker__logs","tool_use_id":"call_1","cwd":"/repo","tool_input":` + toolInput + `}`)
	payload, err := hook.ParseHookPayload(hook.SystemCursor, rawJSON)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.ToolInputCommand != "docker logs api" {
		t.Fatalf("tool_input.command = %#v, want docker logs api", fields.ToolInputCommand)
	}
	if fields.ToolInputQuery != "errors" {
		t.Fatalf("tool_input.query = %#v, want errors", fields.ToolInputQuery)
	}
}

func TestCursorBeforeMCPExecution_MalformedJSONStringToolInput(t *testing.T) {
	toolInput := strconv.Quote(`{"command":"docker logs api"`)
	rawJSON := []byte(`{"hook_event_name":"beforeMCPExecution","session_id":"s1","tool_name":"mcp__docker__logs","tool_use_id":"call_1","cwd":"/repo","tool_input":` + toolInput + `}`)
	payload, err := hook.ParseHookPayload(hook.SystemCursor, rawJSON)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if got := payload.Fields().ToolInputContent; got != `{"command":"docker logs api"` {
		t.Fatalf("tool_input.content = %#v, want malformed JSON string", got)
	}
}

func TestCursorAfterMCPExecution_StringToolInput(t *testing.T) {
	rawJSON := []byte(`{"hook_event_name":"afterMCPExecution","session_id":"s1","tool_name":"mcp__docker__logs","tool_use_id":"call_1","cwd":"/repo","tool_input":"docker logs api","tool_output":"ok"}`)
	payload, err := hook.ParseHookPayload(hook.SystemCursor, rawJSON)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.ToolInputContent != "docker logs api" {
		t.Fatalf("tool_input.content = %#v, want docker logs api", fields.ToolInputContent)
	}
	if fields.ToolOutput != "ok" {
		t.Fatalf("tool_output = %#v, want ok", fields.ToolOutput)
	}
}

func TestVSCodePayload_EditToolInput(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemVSCode, []byte(`{"hook_event_name":"PreToolUse","tool_name":"replace_string_in_file","tool_input":{"filePath":"/project/page.zig","oldString":"old text","newString":"new text"}}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.ToolInputFilePath != "/project/page.zig" {
		t.Fatalf("tool_input.file_path = %#v, want /project/page.zig", fields.ToolInputFilePath)
	}
	if fields.ToolInputOldString != "old text" {
		t.Fatalf("tool_input.old_string = %#v, want old text", fields.ToolInputOldString)
	}
	if fields.ToolInputNewString != "new text" {
		t.Fatalf("tool_input.new_string = %#v, want new text", fields.ToolInputNewString)
	}
}

func TestVSCodePayload_MultiReplaceToolInput(t *testing.T) {
	payload, err := hook.ParseHookPayload(hook.SystemVSCode, []byte(`{"hook_event_name":"PreToolUse","tool_name":"multi_replace_string_in_file","tool_input":{"replacements":[{"filePath":"/project/page.zig","oldString":"first old","newString":"first new"},{"filePath":"/project/list.zig","oldString":"second old","newString":"second new"}]}}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	fields := payload.Fields()
	if fields.ToolInputNewString != "first new\nsecond new" {
		t.Fatalf("tool_input.new_string = %#v, want joined new strings", fields.ToolInputNewString)
	}
	if len(fields.EditsNewString) != 2 || fields.EditsNewString[1] != "second new" {
		t.Fatalf("edits new strings = %#v, want second new", fields.EditsNewString)
	}
}

func TestCopilotPayload_AddsAssistantMessageFromTranscript(t *testing.T) {
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

	payload, err := hook.ParseHookPayload(hook.SystemCopilot, []byte(`{"hook_event_name":"Stop","transcript_path":"`+transcript+`"}`))
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	want := "Final response with em dash " + dash + " blocked."
	if got := payload.Fields().LastAssistantMessage; got != want {
		t.Fatalf("last_assistant_message = %#v, want %#v", got, want)
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
			{Name: "codex-tool-command", CodexEvents: []string{"PreToolUse"}, FieldPaths: []string{"tool_input.command"}},
			{Name: "gemini-tool-command", GeminiEvents: []string{"BeforeTool"}, FieldPaths: []string{"tool_input.command"}},
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
					{Kind: "diff", FieldPair: "tool_input.old_string,tool_input.new_string", Pattern: "y"},
					{Kind: "shell-write", FieldPaths: []string{"tool_input.command"}, Globs: []string{"*.txt"}},
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
