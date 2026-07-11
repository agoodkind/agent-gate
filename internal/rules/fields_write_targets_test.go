package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestCmdWriteTargetsField(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"redirect to repo file", `echo x > out.txt`, "/repo/out.txt"},
		{"append redirect", `echo x >> log.txt`, "/repo/log.txt"},
		{"sed in place", `sed -i s/a/b/ file.go`, "/repo/file.go"},
		{"tee target", `echo x | tee note.md`, "/repo/note.md"},
		{"cd rebases redirect target", `cd /elsewhere && echo x > out.txt`, "/elsewhere/out.txt"},
		{"redirect survives trailing 2>&1", `cd /elsewhere && echo x > out.txt 2>&1`, "/elsewhere/out.txt"},
		{"pure read has no write target", `grep -n x file.go`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := FieldSet{ToolInputCommand: tc.command, CWD: "/repo"}
			got := fields.CmdWriteTargets()
			if got != tc.want {
				t.Fatalf("cmd_write_targets for %q = %q, want %q", tc.command, got, tc.want)
			}
			if got := fields.String(config.FieldCmdWriteTargets); got != tc.want {
				t.Fatalf("generic cmd_write_targets selector for %q = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

func TestCmdWriteTargetsWithSpecsRequiresDeclaration(t *testing.T) {
	fields := FieldSet{ToolInputCommand: "writer-all generated.txt", CWD: "/repo"}
	if got := fields.CmdWriteTargets(); got != "" {
		t.Fatalf("CmdWriteTargets() = %q, want empty without declarations", got)
	}
	specs := []config.ShellWriteSpec{{
		Argv0:      []string{"writer-all"},
		TargetMode: config.WriteTargetAllOperands,
	}}
	if got := fields.CmdWriteTargetsWithSpecs(specs); got != "/repo/generated.txt" {
		t.Fatalf("CmdWriteTargetsWithSpecs() = %q, want /repo/generated.txt", got)
	}
	condition := &config.Condition{WriteSpecs: specs}
	if got := fields.StringForCondition(config.FieldCmdWriteTargets, condition); got != "/repo/generated.txt" {
		t.Fatalf("StringForCondition() = %q, want /repo/generated.txt", got)
	}
}

func TestCmdWriteTargetsWithSpecsDropsUnresolvedOperand(t *testing.T) {
	fields := FieldSet{ToolInputCommand: `writer-all "$TARGET"`, CWD: "/repo"}
	specs := []config.ShellWriteSpec{{
		Argv0:      []string{"writer-all"},
		TargetMode: config.WriteTargetAllOperands,
	}}
	if got := fields.CmdWriteTargetsWithSpecs(specs); got != "" {
		t.Fatalf("CmdWriteTargetsWithSpecs() = %q, want unresolved target dropped", got)
	}
}
