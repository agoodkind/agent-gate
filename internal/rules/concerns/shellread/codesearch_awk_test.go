package shellread

import (
	"slices"
	"testing"
)

// awkTestTools is the tool policy that declares awk as a content reader, so the
// awk region's analyzer-derived in-script reads are folded into the targets.
var awkTestTools = []string{"grep", "rg", "awk", "gawk"}

// TestExtractCodeSearchTargetsAwkInScriptRead covers an awk program whose
// getline reads a literal file: that path is folded into the code-search targets
// when awk is a declared tool, alongside the awk command's own data-file operand
// that the top-level read scan always surfaces. The fold applies the write-guard
// and the unresolvable drop, mirroring the python path. The dropped and
// redirect-only cases surface only the data-file operand, never a fabricated
// in-script path.
func TestExtractCodeSearchTargetsAwkInScriptRead(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"getline literal", `awk '{ getline < "x.go" }' f`, []string{"/repo/f", "/repo/x.go"}},
		{"getline with var", `awk '{ getline line < "y.go" }' f`, []string{"/repo/f", "/repo/y.go"}},
		{"getline absolute", `awk '{ getline < "/abs/z.go" }' f`, []string{"/abs/z.go", "/repo/f"}},
		{"getline variable target dropped", `awk '{ getline < v }' f`, []string{"/repo/f"}},
		{"getline ARGV target dropped", `awk '{ getline < ARGV[1] }' f`, []string{"/repo/f"}},
		{"print redirect is not a read", `awk 'BEGIN { print "x" > "o.txt" }' f`, []string{"/repo/f"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, awkTestTools, nil))
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsAwkToolGate covers the tool gate: an awk program's
// in-script reads are folded only when the rule declares awk (or gawk) as a
// tool. A tool set without awk folds nothing from the awk region.
func TestExtractCodeSearchTargetsAwkToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `awk '{ getline < "x.go" }' f`

	withAwk := targetPaths(ExtractCodeSearchTargets(command, cwd, awkTestTools, nil))
	slices.Sort(withAwk)
	if !slices.Equal(withAwk, []string{"/repo/f", "/repo/x.go"}) {
		t.Fatalf("with awk tool = %v, want [/repo/f /repo/x.go]", withAwk)
	}

	// Without awk in the declared tools, neither the awk data-file operand (the
	// top-level read scan gates on Argv0) nor the in-script getline read is
	// surfaced, so the result is empty.
	withoutAwk := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(withoutAwk) != 0 {
		t.Fatalf("without awk tool = %v, want none", withoutAwk)
	}
}
