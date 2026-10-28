package shellread

import (
	"slices"
	"testing"
)

// enumTestTools omits content readers; explicit enumeration policy selects directories.
var enumTestTools []string

// TestExtractCodeSearchTargetsRecursiveEnumeration covers shell-level recursive
// structure discovery with no content searcher: a recursive enumeration resolves
// the enumerated directory, while a shallow one stays allowed.
func TestExtractCodeSearchTargetsRecursiveEnumeration(t *testing.T) {
	const cwd = "/repo"
	policy := testEnumerationPolicy(t)

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"ls -R names the dir", "ls -R /repo", []string{"/repo"}},
		{"ls combined flags -laR", "ls -laR /repo", []string{"/repo"}},
		{"ls -R no operand is cwd", "ls -R", []string{"/repo"}},
		{"shallow ls allowed", "ls /repo", nil},
		{"find recursive by default", "find /repo", []string{"/repo"}},
		{"find with name is still recursive", "find /repo -name '*.go'", []string{"/repo"}},
		{"find maxdepth 1 is shallow", "find /repo -maxdepth 1", nil},
		{"find maxdepth 1 with name is shallow", "find /repo -maxdepth 1 -name '*.go'", nil},
		{"git ls-files names cwd", "git ls-files", []string{"/repo"}},
		{"recursive glob names base dir", "cat src/**/*.go", []string{"/repo/src"}},
		{"recursive glob at root names cwd", "cat **/*.go", []string{"/repo"}},
		{"bare ** without a slash is not a glob target", "echo 2 ** 3", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargetsWithPolicy(tc.command, cwd, enumTestTools, policy.EnumerationSpecs, policy.RecursiveGlobs, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
