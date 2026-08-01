package rules

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// diffReadSpec expresses `diff a b` positionally: both operands are paths.
// shelldecomp gives diff no reading command kind, so declaring "diff" in
// search_tools resolves nothing; this is the config-only route to the same
// targets.
var diffReadSpec = []config.ShellReadSpec{{
	Name:                        "diff",
	Argv0:                       []string{"diff"},
	PathArgStart:                1,
	PathArgStartIfFlags:         nil,
	PathArgStartIfFlagsValue:    0,
	SkipPositionals:             0,
	SkipFlagsWithValues:         nil,
	SkipFlagValuesAsPositionals: nil,
	NestedCommand:               false,
	NestedCommandFlag:           "",
	NestedRemote:                false,
	RemoteSources:               false,
}}

// TestSearchToolsAloneMissDiff records why read_specs is needed at all: no
// entry in search_tools makes diff resolve a target, because the miss is in
// shelldecomp's command-kind table, not in the tool policy.
func TestSearchToolsAloneMissDiff(t *testing.T) {
	fields := FieldSet{ToolInputCommand: "diff a.go b.go", CWD: "/repo"}
	if got := fields.CmdReadTargets([]string{"diff", "grep"}, nil, nil); got != "" {
		t.Fatalf("search_tools resolved diff targets = %q, want empty", got)
	}
}

// TestReadSpecsResolveDiffTargets is the point of the wiring: a rule can now
// declare a command shape in TOML and get its read targets, with no change to
// shelldecomp and no new Go.
func TestReadSpecsResolveDiffTargets(t *testing.T) {
	fields := FieldSet{ToolInputCommand: "diff a.go b.go", CWD: "/repo"}
	got := fields.CmdReadTargets(nil, diffReadSpec, nil)
	want := []string{"/repo/a.go", "/repo/b.go"}
	for _, path := range want {
		if !strings.Contains(got, path) {
			t.Fatalf("read_specs targets = %q, want to contain %q", got, path)
		}
	}
}

// TestReadSpecsUnionWithSearchTools covers the two sources feeding one
// selector, and covers deduplication when both name the same path.
func TestReadSpecsUnionWithSearchTools(t *testing.T) {
	fields := FieldSet{
		ToolInputCommand: "grep -n x /repo/one.go && diff /repo/one.go /repo/two.go",
		CWD:              "/repo",
	}
	got := fields.CmdReadTargets([]string{"grep"}, diffReadSpec, nil)
	for _, path := range []string{"/repo/one.go", "/repo/two.go"} {
		if !strings.Contains(got, path) {
			t.Fatalf("union targets = %q, want to contain %q", got, path)
		}
	}
	if strings.Count(got, "/repo/one.go") != 1 {
		t.Fatalf("union targets = %q, want /repo/one.go exactly once", got)
	}
}

// TestNeitherSourceYieldsNothing keeps the no-built-in-default contract: a rule
// that declares neither source resolves no targets.
func TestNeitherSourceYieldsNothing(t *testing.T) {
	fields := FieldSet{ToolInputCommand: "diff a.go b.go", CWD: "/repo"}
	if got := fields.CmdReadTargets(nil, nil, nil); got != "" {
		t.Fatalf("targets with neither source = %q, want empty", got)
	}
}
