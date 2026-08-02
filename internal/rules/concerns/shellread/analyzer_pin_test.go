package shellread_test

import (
	"slices"
	"testing"

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
// reach agent-gate, not merely that they exist upstream. It asserts on whichever
// gksyntax source the build actually resolves, which is the workspace copy in a
// worktree and the go.mod version in CI, so a stale source fails here whichever
// one is selected. It does not check the two pins independently.
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
