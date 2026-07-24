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

func TestEvaluateAllRegexConditionMatchesLoopCount(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "loop-count-regex"
cursor_events = ["stop"]
action = "block"
violation_message = "loop count matched"

[[rules.conditions]]
kind = "regex"
field_paths = ["loop_count"]
pattern = "^(0|12)$"
`
	cfg := loadTOML(t, tomlBody)
	zero := 0
	retryCount := 12
	for _, testCase := range []struct {
		name      string
		loopCount *int
		matched   bool
		wantValue string
	}{
		{name: "missing", matched: false},
		{name: "zero", loopCount: &zero, matched: true, wantValue: "0"},
		{name: "later retry", loopCount: &retryCount, matched: true, wantValue: "12"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			violations := rules.EvaluateAll(
				context.Background(),
				"cursor",
				"stop",
				rules.FieldSet{LoopCount: testCase.loopCount},
				cfg.Rules,
				nil,
			)
			if (len(violations) > 0) != testCase.matched {
				t.Fatalf("violations = %#v, want matched=%v", violations, testCase.matched)
			}
			if !testCase.matched {
				return
			}
			if violations[0].FieldPath != "loop_count" ||
				violations[0].Value != testCase.wantValue {
				t.Fatalf(
					"violation = %#v, want loop_count=%q",
					violations[0],
					testCase.wantValue,
				)
			}
		})
	}
}
