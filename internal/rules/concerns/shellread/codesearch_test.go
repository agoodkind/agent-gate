package shellread

import (
	"slices"
	"testing"
)

// testSearchTools is the tool policy these tests exercise; production policy
// lives in each rule's search_tools config.
var testSearchTools = []string{"grep", "egrep", "fgrep", "rg", "ag", "ack", "git grep", "sed"}

func targetPaths(targets []ReadTarget) []string {
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.Path)
	}
	return paths
}

// TestExtractCodeSearchTargetsShelldecompSanity is the post-migration sanity
// check from the shelldecomp integration: an extensionless code grep over a repo
// path resolves; a /tmp log grep resolves but is outside the repo (the
// index-aware validator decides scope); git grep is excluded; a find piped to a
// stdin grep reads filenames only and has no target; and a cd into an
// unexpanded variable poisons the cwd so the recursive grep target is dropped
// rather than fabricated.
func TestExtractCodeSearchTargetsShelldecompSanity(t *testing.T) {
	const cwd = "/repo"
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"extensionless code grep over repo path", `grep -rn ServeHTTP internal`, []string{"/repo/internal"}},
		{"tmp log grep resolves outside repo", `grep -n ERROR /tmp/x.log`, []string{"/tmp/x.log"}},
		{"git grep with pathspec", `git grep ServeHTTP internal`, []string{"/repo/internal"}},
		{"find piped to stdin grep has no target", `find Tests | grep -iE x`, nil},
		{"cd to unresolvable var drops recursive target", `cd "$VAR" && grep -rn X .`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, testSearchTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
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
		{"bare git grep recurses cwd", `git grep "x"`, []string{"/repo"}},
		{"git commit message is not git grep", `git commit -m grep`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, testSearchTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsAwk covers awk as a content reader: an awk
// pattern scan reads its file operand, while the gawk in-place extension edits
// it, so the write guard drops it.
func TestExtractCodeSearchTargetsAwk(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"pattern scan", `awk '/needle/ {print}' internal/x.go`, []string{"/repo/internal/x.go"}},
		{"xargs awk over find", `find Sources | xargs awk '/needle/'`, []string{"/repo/Sources"}},
		{"stdin awk has no target", `cat /tmp/x | awk '{print $1}'`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, []string{"awk", "gawk"}, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsEmbedded covers code search hidden inside
// embedded code: a local wrapper shell's -c script and a heredoc body written
// to a temp script. A remote or containerized wrapper (ssh, docker) reads
// remote paths, so its embedded search names no local target.
func TestExtractCodeSearchTargetsEmbedded(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"bash -c grep", `bash -c "grep -rn needle /repo/internal"`, []string{"/repo/internal"}},
		{"bash -lc rg", `bash -lc "rg -n needle /repo/internal"`, []string{"/repo/internal"}},
		{"sh -c with cd chain", `sh -c 'cd /other && grep -rn needle .'`, []string{"/other"}},
		{"zsh -c relative operand", `zsh -c 'grep -rn needle internal'`, []string{"/repo/internal"}},
		{"heredoc temp script then exec", "cat > /tmp/s.sh <<'EOF'\nrg -n needle /repo/internal\nEOF\nbash /tmp/s.sh", []string{"/repo/internal"}},
		{"mktemp var script then exec", "S=$(mktemp); cat >\"$S\" <<'EOF'\nrg -n needle /repo/internal\nEOF\nbash \"$S\"", []string{"/repo/internal"}},
		{"prose heredoc is not a search", "cat > /tmp/notes.md <<'EOF'\nsome notes about the rg tool\nEOF\n", nil},
		{"ssh remote grep is not local", `ssh host 'grep -rn needle /repo/internal'`, nil},
		{"docker run grep is not local", `docker run img grep -rn needle /repo/internal`, nil},
		{"bash script file alone", `bash /tmp/s.sh`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, testSearchTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsSed covers sed as a content reader: a sed that
// reads a file is a code search the semantic index can answer, while a sed -i
// edits the file in place and is not a search, so its operands are dropped.
func TestExtractCodeSearchTargetsSed(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"line range read", `sed -n '12,40p' file.go`, []string{"/repo/file.go"}},
		{"pattern read", `sed -n '/needle/p' internal/x.go`, []string{"/repo/internal/x.go"}},
		{"stream edit reads its operand", `sed 's/a/b/' file.go`, []string{"/repo/file.go"}},
		{"in-place edit is not a search", `sed -i 's/a/b/' file.go`, nil},
		{"in-place edit with suffix is not a search", `sed -i.bak 's/a/b/' file.go`, nil},
		{"stdin sed has no target", `cat /tmp/x | sed -n '1p'`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, testSearchTools, nil))
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
		// Content search over file contents through xargs or -exec: in scope.
		{"find name piped to xargs grep", `find . -name '*.swift' | xargs grep -l toolchain`, []string{"/repo"}},
		{"find dir piped to xargs grep", `find Sources | xargs grep -nE x`, []string{"/repo/Sources"}},
		{"find prune then name piped to xargs grep", `find . -path ./.build -prune -o -name '*.swift' -print | xargs grep -l x`, []string{"/repo"}},
		{"find exec grep", `find . -name '*.swift' -exec grep -l toolchain {} +`, []string{"/repo"}},
		{"git ls-files piped to xargs rg", `git ls-files | xargs rg toolchain`, []string{"/repo"}},
		{"fd piped to xargs rg", `fd -e swift | xargs rg toolchain`, []string{"/repo"}},
		{"multiple find paths piped to xargs grep", `find Sources Tests | xargs grep x`, []string{"/repo/Sources", "/repo/Tests"}},

		// Filename lookups (read names, not contents): out of scope.
		{"find piped to bare grep filters names", `find Tests | grep -iE "x"`, nil},
		{"git ls-files piped to bare grep filters names", `git ls-files | grep '\.go$'`, nil},
		{"bare find name code ext", `find . -name '*.go'`, nil},
		{"bare find iname code ext", `find Sources -iname '*.swift' -print`, nil},
		{"bare find non-code ext", `find . -name '*.json' -print`, nil},
		{"bare find no name filter", `find Tests -type f`, nil},
		{"git ls-files alone", `git ls-files`, nil},
		{"git grep feeding a pipeline recurses cwd", `git grep x | xargs echo`, []string{"/repo"}},

		// Operand-bearing stages resolve through the operand parser, not here.
		{"enumerator and grep in separate pipelines", `find Sources ; grep x other.txt`, []string{"/repo/other.txt"}},

		// Unresolvable enumerator directory is dropped.
		{"find var dir piped to xargs grep", `find "$dir" -name '*.go' | xargs grep x`, nil},

		// Non-cwd absolute directory resolves; the validator gates on index.
		{"find absolute data dir piped to xargs grep", `find /data -name '*.json' | xargs grep x`, []string{"/data"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, testSearchTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
