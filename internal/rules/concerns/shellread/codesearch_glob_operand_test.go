package shellread

import (
	"slices"
	"testing"
)

// TestExtractCodeSearchTargetsPythonGlobRoot covers the glob-driven content
// search an agent reaches for when it wants to read a repository without naming
// a searcher. The python region's own analyzer resolves the glob root, and the
// shell-level ** scan no longer reads the program's source as shell fields, so
// the enumerated root is the only target.
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

// TestExtractCodeSearchTargetsForeignRegionNotReadAsShell covers the shapes a
// shell-field scan of program source fabricates: a call operand becomes a
// directory that cannot exist, and a language construct opening with a bracket
// is mistaken for a glob wildcard, which resolves the base to cwd and silently
// displaces the directory the program actually reads. Neither may contribute a
// target.
func TestExtractCodeSearchTargetsForeignRegionNotReadAsShell(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
	}{
		{"ruby bracket index is not a glob", `ruby -e 'Dir["/abs/indexed/**/*.rb"].each { |f| puts File.read(f) }'`},
		{"ruby call operand is not a directory", `ruby -e 'Dir.glob "/abs/lmd/**/*.go"'`},
		{"python call operand is not a directory", `python3 -c 'import glob; [open(p).read() for p in glob.glob("/abs/lmd/**/*.go", recursive=True)]'`},
	}

	tools := []string{"grep", "rg", "python", "python3", "ruby"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, target := range targetPaths(ExtractCodeSearchTargets(tc.command, cwd, tools, nil)) {
				if target == cwd {
					t.Fatalf("%q resolved the working directory as a search target", tc.command)
				}
				if !isCleanAbsoluteDir(target) {
					t.Fatalf("%q produced a fabricated target %q", tc.command, target)
				}
			}
		})
	}
}

// isCleanAbsoluteDir reports whether a resolved target looks like a path a shell
// operand could name, rather than a fragment of program source. It exists only
// so the test above can state what a fabricated target looks like.
func isCleanAbsoluteDir(target string) bool {
	if target == "" || target[0] != '/' {
		return false
	}
	for _, r := range target {
		if r == '(' || r == ')' || r == '"' || r == '\'' {
			return false
		}
	}
	return true
}

// TestRecursiveGlobDirsResolvesRealShellOperands covers the operands a real
// shell command writes, including directory names holding characters a
// character-level filter would reject. A duplicated macOS download lands at a
// name with parentheses, and dropping it removes the only layer that names that
// directory for a command whose argv0 is not a declared searcher.
func TestRecursiveGlobDirsResolvesRealShellOperands(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"relative glob", "cat src/**/*.go", []string{"/repo/src"}},
		{"absolute glob", "cat /abs/pkg/**/*.go", []string{"/abs/pkg"}},
		{"glob at root", "cat **/*.go", []string{"/repo"}},
		{"parenthesized directory", `cat "/abs/My Repo (1)/**/*.go"`, []string{"/abs/My Repo (1)"}},
		{"escaped space directory", `cat /abs/My\ Repo/**/*.go`, []string{"/abs/My Repo"}},
		{"comma directory", "cat /abs/a,b/**/*.go", []string{"/abs/a,b"}},
		{"equals directory", "cat /abs/pkg=v1/**/*.go", []string{"/abs/pkg=v1"}},
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

// TestMaskEmbeddedRegionsKeepsShellRegions covers the enumerator layer's
// dependency on nested shell staying visible: the searcher in `xargs rg` and in
// `find -exec grep` is carried as a nested shell region, and blanking it would
// hide the searcher that makes the enumerated directory a content-search target.
func TestMaskEmbeddedRegionsKeepsShellRegions(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"xargs searcher", `fd -e swift | xargs rg toolchain`, []string{"/repo"}},
		{"find exec searcher", `find Sources -name '*.go' -exec grep -l x {} +`, []string{"/repo/Sources"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, enumTestTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}
