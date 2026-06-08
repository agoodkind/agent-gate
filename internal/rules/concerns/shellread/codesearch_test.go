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

// TestExtractCodeSearchTargetsEnumerator covers code search that hides behind an
// enumerator (find/fd/git ls-files), where the searcher reads stdin or find runs
// the searcher itself, so the operand parser sees no path. The enumerated
// directory is the effective target; the index-aware validator decides scope, so
// a resolved non-code or non-repo directory here is correct, not a leak.
func TestExtractCodeSearchTargetsEnumerator(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		// Content search laundered through a pipe or -exec.
		{"find name piped to xargs grep", `find . -name '*.swift' | xargs grep -l toolchain`, []string{"/repo"}},
		{"find dir piped to xargs grep", `find Sources | xargs grep -nE x`, []string{"/repo/Sources"}},
		{"find prune then name piped to xargs grep", `find . -path ./.build -prune -o -name '*.swift' -print | xargs grep -l x`, []string{"/repo"}},
		{"find exec grep", `find . -name '*.swift' -exec grep -l toolchain {} +`, []string{"/repo"}},
		{"find piped to bare grep filters names", `find Tests | grep -iE "x"`, []string{"/repo/Tests"}},
		{"git ls-files piped to xargs rg", `git ls-files | xargs rg toolchain`, []string{"/repo"}},
		{"fd piped to xargs rg", `fd -e swift | xargs rg toolchain`, []string{"/repo"}},
		{"multiple find paths piped to grep", `find Sources Tests | xargs grep x`, []string{"/repo/Sources", "/repo/Tests"}},

		// Bare filename enumeration of code files (no searcher).
		{"bare find name code ext", `find . -name '*.go'`, []string{"/repo"}},
		{"bare find iname code ext uppercase", `find Sources -iname '*.SWIFT' -print`, []string{"/repo/Sources"}},
		{"bare find name code ext lowercase", `find Sources -iname '*.swift' -print`, []string{"/repo/Sources"}},

		// Out of scope: no enumerator-driven code search.
		{"bare find non-code ext", `find . -name '*.json' -print`, nil},
		{"bare find no name filter", `find Tests -type f`, nil},
		{"enumerator and grep in separate pipelines", `find Sources ; grep x other.txt`, []string{"/repo/other.txt"}},
		{"git ls-files alone", `git ls-files`, nil},
		{"git grep is not gated", `git grep x | xargs echo`, nil},

		// Unresolvable enumerator directory is dropped.
		{"find var dir piped to grep", `find "$dir" -name '*.go' | xargs grep x`, nil},

		// Non-cwd absolute directory resolves; the validator gates on index.
		{"find absolute data dir piped to grep", `find /data -name '*.json' | xargs grep x`, []string{"/data"}},
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
