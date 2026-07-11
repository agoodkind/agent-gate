package config_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestLoadWriteSpecs(t *testing.T) {
	setConfigHome(t, `
[[rules]]
name = "declared-writers"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "git_default_branch"
field_paths = ["cmd_write_targets"]

[[rules.conditions.write_specs]]
argv0 = ["writer-all", "writer-create"]
target_mode = "all_operands"
skip_flags_with_values = ["--reference"]
end_of_options = true
cwd_flags = ["--cwd"]

[[rules.conditions.write_specs]]
argv0 = ["writer-copy", "writer-move"]
target_mode = "last_operand"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	specs := cfg.Rules[0].Conditions[0].WriteSpecs
	if len(specs) != 2 {
		t.Fatalf("WriteSpecs length = %d, want 2", len(specs))
	}
	if specs[0].TargetMode != config.WriteTargetAllOperands || !specs[0].EndOfOptions {
		t.Fatalf("first WriteSpec = %#v", specs[0])
	}
	if specs[1].TargetMode != config.WriteTargetLastOperand {
		t.Fatalf("second target mode = %q", specs[1].TargetMode)
	}
}

func TestLoadRejectsInvalidWriteSpecs(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		want      string
	}{
		{
			name: "missing argv0",
			condition: `kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
target_mode = "all_operands"`,
			want: "argv0 is required",
		},
		{
			name: "unknown target mode",
			condition: `kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "first_operand"`,
			want: "unknown target_mode",
		},
		{
			name: "empty flag entry",
			condition: `kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
skip_flags_with_values = [""]`,
			want: "flag entries must be non-empty",
		},
		{
			name: "condition cannot consume write targets",
			condition: `kind = "command"
argv0 = "version-control"
[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"`,
			want: "write_specs require cmd_write_targets",
		},
		{
			name: "overlapping flag roles",
			condition: `kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
skip_flags_with_values = ["--directory"]
cwd_flags = ["--directory"]`,
			want: "must not appear in both skip_flags_with_values and cwd_flags",
		},
		{
			name: "non-flag cwd entry",
			condition: `kind = "git_default_branch"
field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
argv0 = ["writer-all"]
target_mode = "all_operands"
cwd_flags = ["directory"]`,
			want: "cwd_flags entries must start with '-'",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setConfigHome(t, `
[[rules]]
name = "invalid-writer"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
[[rules.conditions]]
`+test.condition)
			_, err := config.Load()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
			const wantContext = `rule "invalid-writer" condition 0 write_specs 0`
			if !strings.Contains(err.Error(), wantContext) {
				t.Fatalf("Load() error = %v, want context %q", err, wantContext)
			}
		})
	}
}
