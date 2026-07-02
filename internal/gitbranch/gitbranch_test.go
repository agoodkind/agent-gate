package gitbranch

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// git runs a git command in dir with an isolated config so the host's global
// git config and hooks cannot interfere with the fixture repos.
func runGit(t *testing.T, dir string, args ...string) {
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

func initRepo(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "f.txt")
	runGit(t, dir, "commit", "-qm", "init")
}

func assert(t *testing.T, label string, gotMatch, gotResolved, wantMatch, wantResolved bool) {
	t.Helper()
	if gotMatch != wantMatch || gotResolved != wantResolved {
		t.Fatalf("%s: got match=%v resolved=%v, want match=%v resolved=%v",
			label, gotMatch, gotResolved, wantMatch, wantResolved)
	}
}

func TestCheckoutOnDefaultBranch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	match, resolved := OnDefaultBranch(repo)
	assert(t, "checkout on main", match, resolved, true, true)
}

func TestFilePathResolvesRepo(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	// A nonexistent nested file path still resolves the enclosing repo.
	match, resolved := OnDefaultBranch(filepath.Join(repo, "sub", "dir", "new.go"))
	assert(t, "nested new file on main", match, resolved, true, true)
}

func TestFeatureBranchNotDefault(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "feature")
	match, resolved := OnDefaultBranch(repo)
	assert(t, "feature branch", match, resolved, false, true)
}

func TestLinkedWorktreeOnFeatureBranch(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	wt := filepath.Join(repo, ".claude", "worktrees", "task")
	if err := os.MkdirAll(filepath.Dir(wt), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	match, resolved := OnDefaultBranch(filepath.Join(wt, "f.txt"))
	assert(t, "worktree on feature", match, resolved, false, true)
}

func TestLinkedWorktreeOnDefaultBranch(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "-b", "dev")
	wt := filepath.Join(base, "wt-main")
	runGit(t, repo, "worktree", "add", "-q", wt, "main")
	match, resolved := OnDefaultBranch(wt)
	assert(t, "worktree on main", match, resolved, true, true)
}

func TestCloneOnFeatureBranch(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	clone := filepath.Join(base, "clone")
	runGit(t, base, "clone", "-q", repo, clone)
	runGit(t, clone, "checkout", "-q", "-b", "feature")
	match, resolved := OnDefaultBranch(clone)
	assert(t, "clone on feature", match, resolved, false, true)
}

func TestCloneReadsOriginHead(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	initRepo(t, repo)
	// Rename the source default branch to a non-conventional name so the clone's
	// default can only be resolved through origin/HEAD; the main/master/trunk
	// fallback cannot find it, which is what makes this test exercise origin/HEAD.
	runGit(t, repo, "branch", "-m", "release")
	clone := filepath.Join(base, "clone")
	runGit(t, base, "clone", "-q", repo, clone)
	match, resolved := OnDefaultBranch(clone)
	assert(t, "clone on non-conventional default via origin/HEAD", match, resolved, true, true)
}

func TestDetachedHead(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepo(t, repo)
	runGit(t, repo, "checkout", "-q", "--detach")
	match, resolved := OnDefaultBranch(repo)
	assert(t, "detached HEAD", match, resolved, false, true)
}

func TestNoRepo(t *testing.T) {
	match, resolved := OnDefaultBranch(t.TempDir())
	assert(t, "outside any repo", match, resolved, false, false)
}
