package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestCommandConditionCwdsExpandsLiteralAssignment(t *testing.T) {
	condition := &config.Condition{
		Argv0:       "git",
		Subcommands: []string{"status"},
		CwdFlags:    []string{"-C"},
	}
	fields := FieldSet{
		CWD:              "/tmp",
		ToolInputCommand: `R=/repo/main; git -C "$R" status`,
	}

	cwds, matched := commandConditionCwds(fields, condition)
	if !matched || len(cwds) != 1 || cwds[0] != "/repo/main" {
		t.Fatalf("commandConditionCwds() = %v, %v; want [/repo/main], true", cwds, matched)
	}
}

func TestCommandConditionCwdsLeavesParameterExpansionUnresolved(t *testing.T) {
	condition := &config.Condition{
		Argv0:       "git",
		Subcommands: []string{"status"},
		CwdFlags:    []string{"-C"},
	}
	fields := FieldSet{
		CWD:              "/tmp",
		ToolInputCommand: `R=/repo/main; git -C "${R:-/tmp}" status`,
	}

	cwds, matched := commandConditionCwds(fields, condition)
	if matched || len(cwds) != 0 {
		t.Fatalf("commandConditionCwds() = %v, %v; want nil, false", cwds, matched)
	}
}
