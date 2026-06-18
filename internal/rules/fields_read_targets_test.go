package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// readTargetsTestTools is the tool policy these tests exercise; production
// policy lives in each rule's search_tools config.
var readTargetsTestTools = []string{"grep", "rg"}

func TestCmdReadTargetsField(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"explicit repo file", `grep -n "x" Package.swift`, "/repo/Package.swift"},
		{"tmp log target", `grep -nE "x" /tmp/swiftmk_check.log`, "/tmp/swiftmk_check.log"},
		{"recursive falls back to cwd", `grep -rn "x"`, "/repo"},
		{"cd rebases recursive target to cd dir", `cd /elsewhere && grep -rn "x" .`, "/elsewhere"},
		{"cd rebases bare rg target to cd dir", `cd /elsewhere && rg "x"`, "/elsewhere"},
		{"cd to unresolvable var drops the target", `cd "$VAR" && grep -rn "x" .`, ""},
		{"find piped to bare grep enumerates its dir", `find Tests | grep -iE "x"`, "/repo/Tests"},
		{"find name piped to xargs grep targets cwd", `find . -name '*.swift' | xargs grep -l x`, "/repo"},
		{"unexpanded var dropped", `grep -n "x" "$dir/a.go"`, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := FieldSet{ToolInputCommand: tc.command, CWD: "/repo"}
			got := fields.CmdReadTargets(readTargetsTestTools, nil)
			if got != tc.want {
				t.Fatalf("cmd_read_targets for %q = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// The tool set is rule policy with no built-in default: without search_tools
// there are no read targets, and the generic field selector (which has no rule
// context) yields nothing.
func TestCmdReadTargetsRequiresDeclaredTools(t *testing.T) {
	fields := FieldSet{ToolInputCommand: `grep -rn "x" .`, CWD: "/repo"}
	if got := fields.CmdReadTargets(nil, nil); got != "" {
		t.Fatalf("CmdReadTargets(nil, nil) = %q, want empty", got)
	}
	if got := fields.String(config.FieldCmdReadTargets); got != "" {
		t.Fatalf("generic cmd_read_targets selector = %q, want empty", got)
	}
}
