package shellread

import (
	"slices"
	"testing"
)

// sedTestTools is the tool policy that declares sed as a content reader, so the
// sed region's analyzer-derived in-script reads are folded into the targets.
var sedTestTools = []string{"grep", "rg", "sed"}

// TestExtractCodeSearchTargetsSedInScriptRead covers a sed script whose r
// command reads a literal file: that path is folded into the code-search targets
// when sed is a declared tool, alongside the sed command's own data-file operand
// that the top-level read scan surfaces. A w write command is not a read, and a
// w inside an s/// regex is not a file, so those surface only the data operand.
func TestExtractCodeSearchTargetsSedInScriptRead(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"r literal", `sed 'r inc.txt' f`, []string{"/repo/f", "/repo/inc.txt"}},
		{"address r", `sed '/p/r inc.txt' f`, []string{"/repo/f", "/repo/inc.txt"}},
		{"r absolute", `sed 'r /abs/inc.txt' f`, []string{"/abs/inc.txt", "/repo/f"}},
		{"w write is not a read", `sed -n 'w out.txt' f`, []string{"/repo/f"}},
		{"s flag w write is not a read", `sed 's/a/b/w log.txt' f`, []string{"/repo/f"}},
		{"w in regex is not a file", `sed 's/w/x/g' f`, []string{"/repo/f"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, sedTestTools, nil))
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsSedToolGate covers the tool gate: a sed script's
// in-script reads fold only when the rule declares sed. A tool set without sed
// folds nothing from the sed region and gates out the sed data operand too.
func TestExtractCodeSearchTargetsSedToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `sed 'r inc.txt' f`

	withSed := targetPaths(ExtractCodeSearchTargets(command, cwd, sedTestTools, nil))
	slices.Sort(withSed)
	if !slices.Equal(withSed, []string{"/repo/f", "/repo/inc.txt"}) {
		t.Fatalf("with sed tool = %v, want [/repo/f /repo/inc.txt]", withSed)
	}

	withoutSed := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(withoutSed) != 0 {
		t.Fatalf("without sed tool = %v, want none", withoutSed)
	}
}
