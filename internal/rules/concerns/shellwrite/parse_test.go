package shellwrite_test

import (
	"testing"

	shellwriteconcern "goodkind.io/agent-gate/internal/rules/concerns/shellwrite"
)

// findTarget returns the first target whose Path equals path.
func findTarget(targets []shellwriteconcern.WriteTarget, path string) (shellwriteconcern.WriteTarget, bool) {
	for _, target := range targets {
		if target.Path == path {
			return target, true
		}
	}
	return shellwriteconcern.WriteTarget{Path: "", Tool: "", Reason: "", Raw: ""}, false
}

func TestExtractWriteTargets_AppendRedirect(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo hi >> /tmp/log", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %#v", len(targets), targets)
	}
	if targets[0].Path != "/tmp/log" {
		t.Errorf("Path = %q, want /tmp/log", targets[0].Path)
	}
	if targets[0].Tool != shellwriteconcern.ToolRedirect {
		t.Errorf("Tool = %q, want %q", targets[0].Tool, shellwriteconcern.ToolRedirect)
	}
	if targets[0].Reason != shellwriteconcern.ReasonOK {
		t.Errorf("Reason = %q, want %q", targets[0].Reason, shellwriteconcern.ReasonOK)
	}
}

func TestExtractWriteTargets_OverwriteRedirect(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo hi > /tmp/log", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Path != "/tmp/log" {
		t.Errorf("Path = %q, want /tmp/log", targets[0].Path)
	}
}

func TestExtractWriteTargets_TeeAppend(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo hi | tee -a /tmp/log", "/cwd")
	if _, ok := findTarget(targets, "/tmp/log"); !ok {
		t.Fatalf("expected /tmp/log target via tee -a, got %#v", targets)
	}
}

func TestExtractWriteTargets_TeePositional(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo hi | tee /tmp/log /tmp/log2", "/cwd")
	if _, ok := findTarget(targets, "/tmp/log"); !ok {
		t.Fatalf("expected /tmp/log target, got %#v", targets)
	}
	if _, ok := findTarget(targets, "/tmp/log2"); !ok {
		t.Fatalf("expected /tmp/log2 target, got %#v", targets)
	}
}

func TestExtractWriteTargets_SedInPlace(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("sed -i 's/a/b/' /tmp/file", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %#v", len(targets), targets)
	}
	if targets[0].Path != "/tmp/file" {
		t.Errorf("Path = %q, want /tmp/file", targets[0].Path)
	}
	if targets[0].Tool != shellwriteconcern.ToolSed {
		t.Errorf("Tool = %q, want sed", targets[0].Tool)
	}
}

func TestExtractWriteTargets_SedInPlaceWithSuffix(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("sed -i.bak 's/a/b/' /tmp/file", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d: %#v", len(targets), targets)
	}
	if targets[0].Path != "/tmp/file" {
		t.Errorf("Path = %q, want /tmp/file", targets[0].Path)
	}
}

func TestExtractWriteTargets_SedNoInPlace(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("sed 's/a/b/' /tmp/file", "/cwd")
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for read-only sed, got %d: %#v", len(targets), targets)
	}
}

func TestExtractWriteTargets_AwkInPlace(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("awk -i inplace '{print}' /tmp/file", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Tool != shellwriteconcern.ToolAwk {
		t.Errorf("Tool = %q, want awk", targets[0].Tool)
	}
}

func TestExtractWriteTargets_Patch(t *testing.T) {
	// patch /tmp/file (no input redirect) resolves to the exact path.
	targets := shellwriteconcern.ExtractWriteTargets("patch /tmp/file", "/cwd")
	if _, ok := findTarget(targets, "/tmp/file"); !ok {
		t.Fatalf("expected /tmp/file target via patch, got %#v", targets)
	}
}

// TestExtractWriteTargets_PatchWithInputRedirect verifies that an input redirect
// (< diff.patch) does not suppress patch's inline write to its target file.
func TestExtractWriteTargets_PatchWithInputRedirect(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("patch /tmp/file < diff.patch", "/cwd")
	if _, ok := findTarget(targets, "/tmp/file"); !ok {
		t.Fatalf("expected /tmp/file write target despite the input redirect, got %#v", targets)
	}
}

func TestExtractWriteTargets_PatchWithFlag(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("patch -p1 /tmp/file", "/cwd")
	if _, ok := findTarget(targets, "/tmp/file"); !ok {
		t.Fatalf("expected /tmp/file target via patch -p1, got %#v", targets)
	}
}

func TestExtractWriteTargets_GitApply(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("git apply /tmp/diff.patch", "/cwd")
	if _, ok := findTarget(targets, "/tmp/diff.patch"); !ok {
		t.Fatalf("expected /tmp/diff.patch target via git apply, got %#v", targets)
	}
}

func TestExtractWriteTargets_GitApplyIndex(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("git apply --index /tmp/diff.patch", "/cwd")
	if _, ok := findTarget(targets, "/tmp/diff.patch"); !ok {
		t.Fatalf("expected /tmp/diff.patch via git apply --index, got %#v", targets)
	}
}

func TestExtractWriteTargets_HeredocRedirect(t *testing.T) {
	// An output redirect after a heredoc operator (cat <<EOF >> /tmp/log) parses
	// as a file_redirect nested inside the heredoc_redirect; the write must still
	// be recorded.
	cmd := "cat <<EOF >> /tmp/log\nhello\nEOF"
	targets := shellwriteconcern.ExtractWriteTargets(cmd, "/cwd")
	if _, ok := findTarget(targets, "/tmp/log"); !ok {
		t.Fatalf("expected /tmp/log target from heredoc redirect, got %#v", targets)
	}
}

func TestExtractWriteTargets_RelativePathResolved(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo hi >> baseline.txt", "/work/proj")
	if _, ok := findTarget(targets, "/work/proj/baseline.txt"); !ok {
		t.Fatalf("expected resolved path /work/proj/baseline.txt, got %#v", targets)
	}
}

func TestExtractWriteTargets_EvalIsUnparseable(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("eval $cmd", "/cwd")
	if len(targets) != 1 {
		t.Fatalf("expected 1 sentinel target, got %d", len(targets))
	}
	if targets[0].Reason != shellwriteconcern.ReasonUnparsedCommandShape {
		t.Errorf("Reason = %q, want %q", targets[0].Reason, shellwriteconcern.ReasonUnparsedCommandShape)
	}
}

func TestExtractWriteTargets_BashCIsUnparseable(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets(`bash -c "echo hi >> /tmp/log"`, "/cwd")
	if len(targets) == 0 {
		t.Fatalf("expected sentinel target from bash -c, got none")
	}
	if targets[0].Reason != shellwriteconcern.ReasonUnparsedCommandShape {
		t.Errorf("first Reason = %q, want %q", targets[0].Reason, shellwriteconcern.ReasonUnparsedCommandShape)
	}
}

func TestExtractWriteTargets_CommandSubstitutionUnparseable(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("echo $(date) >> /tmp/log", "/cwd")
	if len(targets) == 0 {
		t.Fatalf("expected sentinel target from $() substitution, got none")
	}
	if targets[0].Reason != shellwriteconcern.ReasonUnparsedCommandShape {
		t.Errorf("Reason = %q, want %q", targets[0].Reason, shellwriteconcern.ReasonUnparsedCommandShape)
	}
}

func TestExtractWriteTargets_ReadOnlyCatNoTargets(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("cat /tmp/file", "/cwd")
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for read-only cat, got %d", len(targets))
	}
}

func TestExtractWriteTargets_GitDiffNoTargets(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargets("git diff /tmp/file", "/cwd")
	if len(targets) != 0 {
		t.Fatalf("expected 0 targets for git diff, got %d", len(targets))
	}
}

func TestExtractWriteTargets_EmptyInput(t *testing.T) {
	if got := shellwriteconcern.ExtractWriteTargets("", "/cwd"); got != nil {
		t.Fatalf("expected nil for empty cmd, got %v", got)
	}
}

func TestExtractWriteTargets_ProcessSubstitutionIsNotAWrite(t *testing.T) {
	// `> >(cmd)` is process substitution, not a file write. The redirection
	// destination is a subshell, not a path on disk.
	targets := shellwriteconcern.ExtractWriteTargets("cmd > >(tee log)", "/cwd")
	for _, target := range targets {
		if target.Tool == shellwriteconcern.ToolRedirect && target.Path != "" {
			t.Errorf("redirection treated as file write incorrectly: %#v", target)
		}
	}
}
