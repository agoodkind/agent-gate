package shellread_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
)

func gitShowSpec(refPath bool) config.ShellReadSpec {
	return config.ShellReadSpec{
		Name:            "git-show",
		Argv0:           []string{"git"},
		PathArgStart:    1,
		SkipPositionals: 1,
		RefPathOperands: refPath,
	}
}

func firstPath(targets []shellread.ReadTarget) string {
	if len(targets) == 0 {
		return ""
	}
	return targets[0].Path
}

// TestRefPathOperandResolvesTheFileNotTheRevision is the regression for the
// git REF:PATH shape. `git show HEAD:go.mod` reads go.mod, but resolving the
// whole operand produced /repo/HEAD:go.mod, a path that exists nowhere: the
// real read went unseen and a phantom target reached the validator.
func TestRefPathOperandResolvesTheFileNotTheRevision(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"show at a ref", "git show HEAD:go.mod", "/repo/go.mod"},
		{"show at a sha", "git show 1a2b3c4:internal/config/config.go", "/repo/internal/config/config.go"},
		{"show at a branch", "git show origin/main:README.md", "/repo/README.md"},
		{"cat-file at a ref", "git cat-file -p HEAD:go.sum", "/repo/go.sum"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			targets := shellread.ExtractReadTargets(
				testCase.command, "/repo", []config.ShellReadSpec{gitShowSpec(true)},
			)
			if got := firstPath(targets); got != testCase.want {
				t.Fatalf("path = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRefPathOperandLeavesOrdinaryOperandsAlone keeps the split narrow. A path
// that merely contains a colon is still a path, and a spec that does not
// declare the shape never splits at all.
func TestRefPathOperandLeavesOrdinaryOperandsAlone(t *testing.T) {
	cases := []struct {
		name    string
		command string
		spec    config.ShellReadSpec
		want    string
	}{
		{"plain relative path", "git show notes.md", gitShowSpec(true), "/repo/notes.md"},
		{"absolute path with a colon", "git show /repo/od:d.txt", gitShowSpec(true), "/repo/od:d.txt"},
		{"explicit relative path", "git show ./od:d.txt", gitShowSpec(true), "/repo/od:d.txt"},
		{"trailing colon", "git show HEAD:", gitShowSpec(true), "/repo/HEAD:"},
		{"leading colon", "git show :staged.txt", gitShowSpec(true), "/repo/:staged.txt"},
		{"spec does not declare the shape", "git show HEAD:go.mod", gitShowSpec(false), "/repo/HEAD:go.mod"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			targets := shellread.ExtractReadTargets(
				testCase.command, "/repo", []config.ShellReadSpec{testCase.spec},
			)
			if got := firstPath(targets); got != testCase.want {
				t.Fatalf("path = %q, want %q", got, testCase.want)
			}
		})
	}
}
