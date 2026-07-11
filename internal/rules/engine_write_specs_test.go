package rules_test

import (
	"context"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

func TestEvaluateAllRegexUsesConditionWriteSpecs(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "declared-write-regex"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "declared write matched"

[[rules.conditions]]
kind = "regex"
field_paths = ["cmd_write_targets"]
pattern = "generated[.]txt$"

[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
`
	cfg := loadTOML(t, tomlBody)
	cwd := t.TempDir()
	fields := rules.FieldSet{
		ToolName:         "Bash",
		ToolInputCommand: "writer-all generated.txt",
		CWD:              cwd,
	}
	got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
	if len(got) != 1 {
		t.Fatalf("violations = %#v, want one declared write regex match", got)
	}
	wantTarget := filepath.Join(cwd, "generated.txt")
	if got[0].FieldPath != "cmd_write_targets" || got[0].Value != wantTarget {
		t.Fatalf("violation = %#v, want target %q", got[0], wantTarget)
	}
}
