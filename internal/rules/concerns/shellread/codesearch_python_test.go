package shellread

import (
	"slices"
	"testing"

	"goodkind.io/gksyntax/shelldecomp"
)

// pythonTestTools is the tool policy that declares python as a content reader,
// so the python region's analyzer-derived reads are folded into the targets.
var pythonTestTools = []string{"grep", "rg", "python", "python3"}

// fakeShellReadResolver builds a shelldecomp.FileResolver backed by an in-memory
// map so a test can exercise off-disk script reads without touching disk.
func fakeShellReadResolver(files map[string]string) shelldecomp.FileResolver {
	return func(absPath string) ([]byte, bool) {
		content, ok := files[absPath]
		if !ok {
			return nil, false
		}
		return []byte(content), true
	}
}

// TestExtractCodeSearchTargetsPythonInline covers a python -c program whose
// open() reads are folded into the code-search targets when python is a declared
// tool. The fold applies the write-guard and the unresolvable drop, mirroring
// the structural path.
func TestExtractCodeSearchTargetsPythonInline(t *testing.T) {
	const cwd = "/repo"

	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"argv read", `python -c "open(sys.argv[1])" /repo/x.go`, []string{"/repo/x.go"}},
		{"absolute open", `python -c "open('/abs/y.go')"`, []string{"/abs/y.go"}},
		{"relative open rebased to cwd", `python -c "open('rel.go')"`, []string{"/repo/rel.go"}},
		{"f-string drops the path", `python -c "open(f'{x}.go')"`, nil},
		{"import only has no read", `python -c "import os"`, nil},
		{"write-mode open is not a read", `python -c "open('/r/o.go','w')"`, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := targetPaths(ExtractCodeSearchTargets(tc.command, cwd, pythonTestTools, nil))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ExtractCodeSearchTargets(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

// TestExtractCodeSearchTargetsPythonOffDisk covers a python script file read off
// disk through the resolver: the program inside the file is parsed and its reads
// are folded. Without the resolver the script body is never read, so no read is
// surfaced.
func TestExtractCodeSearchTargetsPythonOffDisk(t *testing.T) {
	const cwd = "/repo"
	resolver := fakeShellReadResolver(map[string]string{
		"/repo/s.py": `open("/repo/y.go")`,
	})

	withResolver := targetPaths(ExtractCodeSearchTargets(`python /repo/s.py`, cwd, pythonTestTools, resolver))
	if !slices.Equal(withResolver, []string{"/repo/y.go"}) {
		t.Fatalf("with resolver = %v, want [/repo/y.go]", withResolver)
	}

	withoutResolver := targetPaths(ExtractCodeSearchTargets(`python /repo/s.py`, cwd, pythonTestTools, nil))
	if len(withoutResolver) != 0 {
		t.Fatalf("without resolver = %v, want none (script body unread)", withoutResolver)
	}
}

// TestExtractCodeSearchTargetsPythonToolGate covers the tool gate: a python
// program's reads are folded only when the rule declares python (or python3) as
// a tool. A tool set without python folds nothing from the python region.
func TestExtractCodeSearchTargetsPythonToolGate(t *testing.T) {
	const cwd = "/repo"
	command := `python -c "open('/repo/x.go')"`

	withPython := targetPaths(ExtractCodeSearchTargets(command, cwd, pythonTestTools, nil))
	if !slices.Equal(withPython, []string{"/repo/x.go"}) {
		t.Fatalf("with python tool = %v, want [/repo/x.go]", withPython)
	}

	withoutPython := targetPaths(ExtractCodeSearchTargets(command, cwd, []string{"grep", "rg"}, nil))
	if len(withoutPython) != 0 {
		t.Fatalf("without python tool = %v, want none", withoutPython)
	}
}

// TestExtractCodeSearchTargetsPythonSubprocess covers a python program that
// shells out: the subprocess command's reads are produced inside the python
// region and folded, so a python -c that runs grep over a repo path surfaces
// that path.
func TestExtractCodeSearchTargetsPythonSubprocess(t *testing.T) {
	const cwd = "/repo"
	command := `python -c "import subprocess; subprocess.run(['grep','TODO','/repo/x.go'])"`
	got := targetPaths(ExtractCodeSearchTargets(command, cwd, pythonTestTools, nil))
	if !slices.Equal(got, []string{"/repo/x.go"}) {
		t.Fatalf("subprocess fold = %v, want [/repo/x.go]", got)
	}
}
