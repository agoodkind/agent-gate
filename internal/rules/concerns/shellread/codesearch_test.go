package shellread

import (
	"slices"
	"testing"
)

func targetPaths(targets []ReadTarget) []string {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	return paths
}

func TestExtractCodeSearchTargets(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		// False positives under the old cwd-only check: the real target is
		// outside the indexed repo or there is no file target at all.
		{"tmp log via pattern", `grep -nE "x" /tmp/swiftmk_check.log`, []string{"/tmp/swiftmk_check.log"}},
		{"tmp swift extract", `rg -n "x" /tmp/main-head.swift`, []string{"/tmp/main-head.swift"}},
		{"unexpanded var operands", `grep -n "x" "$tea_dir/tea.go" "$tea_dir/commands.go"`, nil},
		{"other repo absolute", `grep -rn "x" /other/SwiftLM/Sources/InferenceEngine.swift`, []string{"/other/SwiftLM/Sources/InferenceEngine.swift"}},
		{"find piped into grep reads stdin", `find Tests | grep -iE "x"`, nil},

		// Correct blocks: the operand resolves inside the working tree.
		{"recursive with relative dir", `grep -rl "x" Sources --include=*.swift`, []string{"/repo/Sources"}},
		{"explicit repo file", `grep -n "x" Sources/lmd-serve/SwiftLMD.swift`, []string{"/repo/Sources/lmd-serve/SwiftLMD.swift"}},
		{"repo package file", `grep -nE "x" Package.swift`, []string{"/repo/Package.swift"}},

		// Parser edge cases.
		{"pattern via -e then path", `grep -e PATTERN file.go`, []string{"/repo/file.go"}},
		{"context flags skip values", `grep -A 2 -B 2 "x" file.go`, []string{"/repo/file.go"}},
		{"recursive no path falls back to cwd", `grep -rn "x"`, []string{"/repo"}},
		{"bare rg recurses cwd", `rg "x"`, []string{"/repo"}},
		{"git grep is not gated", `git grep "x"`, nil},
		{"git commit message is not git grep", `git commit -m grep`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
