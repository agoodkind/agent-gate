package shellread

import (
	"slices"
	"testing"
)

// TestExtractCodeSearchTargetsPythonGlobRoot covers the glob-driven content
// search an agent reaches for when it wants to read a repository without naming
// a searcher. The python region's own analyzer resolves the glob root, and the
// shell-level ** scan no longer fabricates a directory out of the program's
// source text, so the enumerated root is the only target.
func TestExtractCodeSearchTargetsPythonGlobRoot(t *testing.T) {
	const cwd = "/repo"

	command := "python3 - <<'PY'\n" +
		"import glob\n" +
		"for p in glob.glob(\"/abs/lmd/**/*.go\", recursive=True):\n" +
		"    text = open(p).read()\n" +
		"PY\n"

	got := targetPaths(ExtractCodeSearchTargets(command, cwd, pythonTestTools, nil))
	if !slices.Equal(got, []string{"/abs/lmd"}) {
		t.Fatalf("python glob root = %v, want [/abs/lmd]", got)
	}
}

// TestRecursiveGlobDirsRejectsProgramSourceOperands covers the operand filter
// directly: a real shell glob still resolves its base directory, while an
// operand carrying call syntax from an embedded program's source resolves
// nothing.
func TestRecursiveGlobDirsRejectsProgramSourceOperands(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"shell relative glob", "cat src/**/*.go", []string{"/repo/src"}},
		{"shell absolute glob", "cat /abs/pkg/**/*.go", []string{"/abs/pkg"}},
		{"shell glob at root", "cat **/*.go", []string{"/repo"}},
		{"python call operand", `glob.glob("/abs/lmd/**/*.go", recursive=True)`, nil},
		{"python keyword argument operand", `sorted(glob.iglob(root+"/**/*.go"))`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := recursiveGlobDirs(tc.command, cwd)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("recursiveGlobDirs(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
