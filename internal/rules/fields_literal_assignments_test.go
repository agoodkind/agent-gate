package rules_test

import (
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

func TestCmdReadTargetsExpandsLiteralAssignments(t *testing.T) {
	fields := rules.FieldSet{
		CWD:              "/tmp",
		ToolName:         "Bash",
		ToolInputCommand: `R=/repo/main; grep -rn todo "$R/internal"`,
	}

	if got, want := fields.CmdReadTargets([]string{"grep"}, nil), "/repo/main/internal"; got != want {
		t.Fatalf("CmdReadTargets() = %q, want %q", got, want)
	}
}

func TestCmdReadTargetsLeavesUnsafeAssignmentsUnresolved(t *testing.T) {
	tests := []string{
		`R=/tmp; R=/repo/main; grep -rn todo "$R/internal"`,
		`R=/repo/main; grep -rn todo "${R:-/tmp}/internal"`,
	}
	for _, command := range tests {
		fields := rules.FieldSet{CWD: "/tmp", ToolName: "Bash", ToolInputCommand: command}
		if got := fields.CmdReadTargets([]string{"grep"}, nil); got != "" {
			t.Fatalf("CmdReadTargets(%q) = %q, want empty", command, got)
		}
	}
}

func TestCmdWriteTargetsExpandsOnlySafeLiteralAssignments(t *testing.T) {
	tests := []struct {
		name    string
		command string
		want    string
	}{
		{name: "literal", command: `R=/repo/main; echo x > "$R/main.go"`, want: "/repo/main/main.go"},
		{name: "reassigned", command: `R=/tmp; R=/repo/main; echo x > "$R/main.go"`, want: ""},
		{name: "command substitution", command: `R=$(pwd); echo x > "$R/main.go"`, want: ""},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields := rules.FieldSet{CWD: filepath.Clean("/tmp"), ToolName: "Bash", ToolInputCommand: test.command}
			if got := fields.CmdWriteTargets(); got != test.want {
				t.Fatalf("CmdWriteTargets() = %q, want %q", got, test.want)
			}
		})
	}
}
