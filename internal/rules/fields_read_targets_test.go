package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestCmdReadTargetsField(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"explicit repo file", `grep -n "x" Package.swift`, "/repo/Package.swift"},
		{"tmp log target", `grep -nE "x" /tmp/swiftmk_check.log`, "/tmp/swiftmk_check.log"},
		{"recursive falls back to cwd", `grep -rn "x"`, "/repo"},
		{"stdin grep has no target", `find Tests | grep -iE "x"`, ""},
		{"unexpanded var dropped", `grep -n "x" "$dir/a.go"`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := FieldSet{ToolInputCommand: tc.command, CWD: "/repo"}
			got := fields.String(config.FieldCmdReadTargets)
			if got != tc.want {
				t.Fatalf("cmd_read_targets for %q = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}
