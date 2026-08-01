package shellread_test

import (
	"slices"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
)

func searchPaths(t *testing.T, command string, tools []string) []string {
	t.Helper()
	paths := make([]string, 0)
	for _, target := range shellread.ExtractCodeSearchTargets(command, "/repo", tools, nil) {
		if target.Remote || target.Path == "" {
			continue
		}
		paths = append(paths, target.Path)
	}
	slices.Sort(paths)
	return paths
}

// TestPinnedAnalyzerDoesNotFabricateJqPaths proves the read-analyzer fixes
// reach agent-gate through the gksyntax pin, not merely that they exist
// upstream. A pin moved in go.mod but not in the submodule, or the reverse,
// leaves this failing while the upstream tests all pass.
//
// Each case fabricated a path before the pin moved: jq's --arg shifted every
// later operand, and jq's -e was misread as supplying the program.
func TestPinnedAnalyzerDoesNotFabricateJqPaths(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{
			name:    "two-value flag",
			command: `jq --arg x 1 '.x' /repo/a.json`,
			want:    []string{"/repo/a.json"},
		},
		{
			name:    "exit-status flag supplies no program",
			command: `jq -e '.x' /repo/a.json`,
			want:    []string{"/repo/a.json"},
		},
		{
			name:    "from-file supplies the program",
			command: `jq --from-file /repo/prog.jq /repo/a.json`,
			want:    []string{"/repo/a.json"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := searchPaths(t, testCase.command, []string{"jq"})
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("read targets = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestPinnedAnalyzerStillResolvesRealSearches keeps the pin bump from silently
// weakening the searches the guard depends on.
func TestPinnedAnalyzerStillResolvesRealSearches(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"grep with a path", `grep -rn pattern /repo/internal`, []string{"/repo/internal"}},
		{"grep -e", `grep -e pattern /repo/a.go`, []string{"/repo/a.go"}},
		{"rg with a path", `rg pattern /repo/internal`, []string{"/repo/internal"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := searchPaths(t, testCase.command, []string{"grep", "rg"})
			if !slices.Equal(got, testCase.want) {
				t.Fatalf("read targets = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestPinnedAnalyzerRespectsTheJavaScriptModule proves the second upstream fix
// reaches agent-gate: a name destructured from fs/promises is checked against
// that module rather than the whole fs table, so a call that throws before
// touching a file is no longer recorded as a read.
func TestPinnedAnalyzerRespectsTheJavaScriptModule(t *testing.T) {
	program := `const { readdirSync } = require("fs").promises; readdirSync("/abs/dir")`
	got := searchPaths(t, `node -e '`+program+`'`, []string{"node"})
	if len(got) != 0 {
		t.Fatalf("read targets = %v, want none; readdirSync does not exist on fs.promises", got)
	}

	valid := `const { readFile } = require("fs").promises; readFile("/abs/a.js")`
	got = searchPaths(t, `node -e '`+valid+`'`, []string{"node"})
	if !slices.Equal(got, []string{"/abs/a.js"}) {
		t.Fatalf("read targets = %v, want /abs/a.js", got)
	}
}

// unusedConfigImport keeps the config import honest if the cases above ever
// need a spec-driven variant.
var _ = config.ShellReadSpec{}
