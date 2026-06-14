package shellread

import (
	"slices"
	"testing"
)

// perlTestTools is the tool policy that declares perl as a content reader, so
// the perl line-loop operand reads and the perl region's analyzer-derived
// in-script open reads are folded into the targets.
var perlTestTools = []string{"grep", "rg", "perl"}

// TestExtractCodeSearchTargetsPerlOperandRead covers the perl -n/-p line loop:
// a script-bearing perl command with -n or -p reads each trailing file operand,
// and those reads surface at the top level with Argv0 "perl", so they fold when
// perl is a declared tool. A -e script without -n/-p reads no operand.
func TestExtractCodeSearchTargetsPerlOperandRead(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"dash-ne reads operand", `perl -ne 'print' x.go`, []string{"/repo/x.go"}},
		{"dash-pe reads operand", `perl -pe 's/a/b/' a.go b.go`, []string{"/repo/a.go", "/repo/b.go"}},
		{"clustered -lne reads operand", `perl -lne 'print' y.go`, []string{"/repo/y.go"}},
		{"dash-e without loop reads nothing", `perl -e 'print' z.go`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, perlTestTools, nil))
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsPerlInScriptRead covers a perl -e program whose
// open() reads a literal file: that path is folded into the code-search targets
// when perl is a declared tool. A write open and a dup open surface nothing.
func TestExtractCodeSearchTargetsPerlInScriptRead(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"three-arg read", `perl -e 'open(my $f,"<","y.go")'`, []string{"/repo/y.go"}},
		{"two-arg read", `perl -e 'open(FH,"<in.go")'`, []string{"/repo/in.go"}},
		{"write open is not a read", `perl -e 'open(my $f,">","out.go")'`, nil},
		{"variable path dropped", `perl -e 'open(my $f,"<",$p)'`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, perlTestTools, nil))
			slices.Sort(got)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsPerlToolGate covers the tool gate: a perl
// command's operand reads and in-script open reads fold only when the rule
// declares perl. A tool set without perl folds nothing from the perl command.
func TestExtractCodeSearchTargetsPerlToolGate(t *testing.T) {
	const cwd = "/repo"

	operandCmd := `perl -ne 'print' x.go`
	withPerl := targetPaths(ExtractCodeSearchTargets(operandCmd, cwd, perlTestTools, nil))
	slices.Sort(withPerl)
	if !slices.Equal(withPerl, []string{"/repo/x.go"}) {
		t.Fatalf("with perl tool = %v, want [/repo/x.go]", withPerl)
	}
	withoutPerl := targetPaths(ExtractCodeSearchTargets(operandCmd, cwd, []string{"grep", "rg"}, nil))
	if len(withoutPerl) != 0 {
		t.Fatalf("without perl tool = %v, want none", withoutPerl)
	}

	openCmd := `perl -e 'open(my $f,"<","y.go")'`
	withPerlOpen := targetPaths(ExtractCodeSearchTargets(openCmd, cwd, perlTestTools, nil))
	slices.Sort(withPerlOpen)
	if !slices.Equal(withPerlOpen, []string{"/repo/y.go"}) {
		t.Fatalf("with perl tool (open) = %v, want [/repo/y.go]", withPerlOpen)
	}
	withoutPerlOpen := targetPaths(ExtractCodeSearchTargets(openCmd, cwd, []string{"grep", "rg"}, nil))
	if len(withoutPerlOpen) != 0 {
		t.Fatalf("without perl tool (open) = %v, want none", withoutPerlOpen)
	}
}
