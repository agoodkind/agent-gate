package oracle

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWorktreeStateReadsPorcelainGitState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	base := t.TempDir()
	primary := filepath.Join(base, "repo")
	feature := filepath.Join(base, "feature")
	initStateRepo(t, primary)
	runStateGit(t, primary, "worktree", "add", "-q", "-b", "feature", feature)
	primary = canonicalStatePath(t, primary)
	feature = canonicalStatePath(t, feature)

	state, err := WorktreeState(feature)
	if err != nil {
		t.Fatalf("WorktreeState() error = %v", err)
	}
	if state.PrimaryCheckout != primary {
		t.Fatalf("PrimaryCheckout = %q, want %q", state.PrimaryCheckout, primary)
	}
	if state.DefaultBranch != "main" {
		t.Fatalf("DefaultBranch = %q, want main", state.DefaultBranch)
	}
	if state.CurrentWorktree != feature {
		t.Fatalf("CurrentWorktree = %q, want %q", state.CurrentWorktree, feature)
	}
	if state.CurrentBranch != "feature" {
		t.Fatalf("CurrentBranch = %q, want feature", state.CurrentBranch)
	}
	assertStateWorktree(t, state, primary, "main", true)
	assertStateWorktree(t, state, feature, "feature", false)
}

func canonicalStatePath(t *testing.T, path string) string {
	t.Helper()
	canonicalPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonicalPath
}

func initStateRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runStateGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runStateGit(t, dir, "add", "f.txt")
	runStateGit(t, dir, "commit", "-qm", "init")
	runStateGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runStateGit(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
}

func runStateGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	full := append([]string{
		"-c", "user.email=test@example.com",
		"-c", "user.name=Test",
		"-c", "commit.gpgsign=false",
		"-c", "init.defaultBranch=main",
	}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func assertStateWorktree(
	t *testing.T,
	state State,
	path string,
	branch string,
	isPrimary bool,
) {
	t.Helper()
	for _, worktree := range state.Worktrees {
		if worktree.Path != path {
			continue
		}
		if worktree.Branch != branch || worktree.IsPrimary != isPrimary {
			t.Fatalf(
				"worktree %q = branch %q primary %v, want branch %q primary %v",
				path,
				worktree.Branch,
				worktree.IsPrimary,
				branch,
				isPrimary,
			)
		}
		return
	}
	t.Fatalf("worktree %q not found in %#v", path, state.Worktrees)
}
