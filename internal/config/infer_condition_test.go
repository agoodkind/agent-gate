package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

const inferRulePrefix = `
[[rules]]
name = "infer-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "infer"
endpoint = "127.0.0.1:5401"
layer_name = "classification"
input_field = "tool_input.command"
response_json_field = "decision"
response_json_equals = "block"
`

const validOutputSchema = `{"type":"object","properties":{"decision":{"type":"string"}},"required":["decision"]}`

func TestInferConditionCompilesInlineDeclarationsAndDefaults(t *testing.T) {
	body := inferRulePrefix + `
prompt = "Classify the input"
output_schema = '` + validOutputSchema + `'
`
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	condition := cfg.Rules[0].Conditions[0]
	if condition.Kind != string(config.ConditionKindInfer) {
		t.Fatalf("Kind = %q, want infer", condition.Kind)
	}
	if condition.Prompt != "Classify the input" || condition.OutputSchema != validOutputSchema {
		t.Fatal("inline declarations were not preserved")
	}
	if condition.InputFieldSelector().Selector != config.FieldToolInputCommand {
		t.Fatalf("input selector = %v, want tool_input.command", condition.InputFieldSelector())
	}
	if condition.TimeoutMs != config.DefaultInferTimeoutMs {
		t.Fatalf("timeout_ms = %d, want %d", condition.TimeoutMs, config.DefaultInferTimeoutMs)
	}
	if condition.BlockOn != config.BlockOnMatch || condition.OnError != config.OnErrorOpen {
		t.Fatalf("defaults = (%q, %q), want (%q, %q)", condition.BlockOn, condition.OnError, config.BlockOnMatch, config.OnErrorOpen)
	}
	if condition.CacheKeySelector().Selector != config.FieldToolInputCommand {
		t.Fatalf("cache selector = %v, want input selector", condition.CacheKeySelector())
	}
	if condition.ResponseJSONEqualsValue().StringValue() != "block" {
		t.Fatalf("response scalar = %q, want block", condition.ResponseJSONEqualsValue().StringValue())
	}
}

func TestInferConditionReadsDeclarationFilesRelativeToConfig(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "prompt.txt"), []byte("File prompt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "schema.json"), []byte(validOutputSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	body := inferRulePrefix + `
prompt_file = "prompt.txt"
output_schema_file = "schema.json"
`
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	condition := cfg.Rules[0].Conditions[0]
	if condition.Prompt != "File prompt" || condition.OutputSchema != validOutputSchema {
		t.Fatalf("compiled declarations = (%q, %q)", condition.Prompt, condition.OutputSchema)
	}
}

func TestInferConditionRequiresExactlyOnePromptDeclaration(t *testing.T) {
	tests := []struct {
		name        string
		declaration string
	}{
		{name: "missing", declaration: ""},
		{name: "both", declaration: "prompt = \"inline\"\nprompt_file = \"prompt.txt\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := inferRulePrefix + test.declaration + "output_schema = '" + validOutputSchema + "'\n"
			_, err := writeExecConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), "exactly one of prompt or prompt_file") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestInferConditionRequiresExactlyOneSchemaDeclaration(t *testing.T) {
	tests := []struct {
		name        string
		declaration string
	}{
		{name: "missing", declaration: ""},
		{name: "both", declaration: "output_schema = '{}'\noutput_schema_file = \"schema.json\"\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := inferRulePrefix + "prompt = \"inline\"\n" + test.declaration
			_, err := writeExecConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), "exactly one of output_schema or output_schema_file") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestInferConditionRejectsMissingFilesWithoutDeclarationContents(t *testing.T) {
	secret := "SECRET_PROMPT_PAYLOAD"
	body := inferRulePrefix + `
prompt_file = "` + secret + `.txt"
output_schema = '` + validOutputSchema + `'
`
	_, err := writeExecConfig(t, body)
	if err == nil {
		t.Fatal("LoadExisting succeeded")
	}
	if strings.Contains(err.Error(), "Classify the input") || strings.Contains(err.Error(), validOutputSchema) {
		t.Fatalf("error leaked declaration contents: %v", err)
	}
}

func TestInferConditionRejectsInvalidSchemaJSONWithoutLeakingIt(t *testing.T) {
	invalid := `{"secret":"DO_NOT_LOG"`
	body := inferRulePrefix + "prompt = \"inline\"\noutput_schema = '" + invalid + "'\n"
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "output_schema must be valid JSON") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "DO_NOT_LOG") {
		t.Fatalf("error leaked schema: %v", err)
	}
}

func TestInferConditionRejectsInvalidSelectorsAndScalarPredicate(t *testing.T) {
	tests := []struct {
		name    string
		replace string
		want    string
	}{
		{name: "input", replace: `input_field = "unknown.path"`, want: "input_field"},
		{name: "cache", replace: `cache_key = "unknown.path"`, want: "cache_key"},
		{name: "workspace", replace: `context_workspace_field = "unknown.path"`, want: "context_workspace_field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(inferRulePrefix, `input_field = "tool_input.command"`, test.replace, 1)
			if test.name == "cache" || test.name == "workspace" {
				body = inferRulePrefix + test.replace + "\n"
			}
			if test.name == "workspace" {
				body += "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"127.0.0.1:5402\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"empty\"\n"
			}
			body += "prompt = \"inline\"\noutput_schema = '" + validOutputSchema + "'\n"
			_, err := writeExecConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestInferConditionValidatesPoliciesAndBounds(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{name: "block_on", field: `block_on = "zero"`, want: "block_on"},
		{name: "on_error", field: `on_error = "maybe"`, want: "on_error"},
		{name: "timeout", field: `timeout_ms = 4001`, want: "exceeds max 4000"},
		{name: "ttl", field: `cache_ttl_ms = -1`, want: "cache_ttl_ms"},
		{name: "context source", field: `context_source = "transcript"`, want: "unknown context_source"},
		{name: "context turns", field: "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"x\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_turn_budget = 33\ncontext_on_error = \"empty\"", want: "context_turn_budget"},
		{name: "context chars", field: "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"x\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_max_chars_per_turn = 8001\ncontext_on_error = \"empty\"", want: "context_max_chars_per_turn"},
		{name: "context policy", field: "context_source = \"clyde_recent_turns\"\ncontext_endpoint = \"x\"\ncontext_workspace_field = \"cwd\"\ncontext_session_field = \"session_id\"\ncontext_on_error = \"maybe\"", want: "context_on_error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := inferRulePrefix + test.field + "\nprompt = \"inline\"\noutput_schema = '" + validOutputSchema + "'\n"
			_, err := writeExecConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestInferConditionCompilesClydeContextDefaults(t *testing.T) {
	body := inferRulePrefix + `
prompt = "inline"
output_schema = '` + validOutputSchema + `'
context_source = "clyde_recent_turns"
context_endpoint = "127.0.0.1:5402"
context_workspace_field = "cwd"
context_session_field = "session_id"
context_on_error = "error"
`
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	condition := cfg.Rules[0].Conditions[0]
	if condition.ContextWorkspaceSelector().Selector != config.FieldCWD || condition.ContextSessionSelector().Selector != config.FieldSessionID {
		t.Fatal("context selectors were not compiled")
	}
	if condition.ContextTurnBudget != config.DefaultContextTurnBudget || condition.ContextMaxCharsPerTurn != config.DefaultContextMaxCharsPerTurn {
		t.Fatalf("context defaults = (%d, %d)", condition.ContextTurnBudget, condition.ContextMaxCharsPerTurn)
	}
}
