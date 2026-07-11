package gitbranch

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

const testHash = "1111111111111111111111111111111111111111"

func TestReadStatePrimaryCheckoutFromNestedTarget(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	primary = cleanStatePath(primary)

	state, err := ReadState(filepath.Join(primary, "missing", "new.go"))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.PrimaryCheckout != primary || state.CurrentWorktree != primary {
		t.Fatalf("checkout paths = %q, %q; want %q", state.PrimaryCheckout, state.CurrentWorktree, primary)
	}
	if state.DefaultBranch != "main" || state.CurrentBranch != "main" {
		t.Fatalf("branches = %q, %q; want main, main", state.DefaultBranch, state.CurrentBranch)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].IsPrimary {
		t.Fatalf("Worktrees = %#v; want one primary checkout", state.Worktrees)
	}
}

func TestReadStateLinkedWorktree(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	linked := filepath.Join(t.TempDir(), "feature")
	addLinkedWorktree(t, primary, linked, "feature")
	primary = cleanStatePath(primary)
	linked = cleanStatePath(linked)

	state, err := ReadState(filepath.Join(linked, "src", "new.go"))
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.PrimaryCheckout != primary || state.CurrentWorktree != linked {
		t.Fatalf("checkout paths = %q, %q; want %q, %q", state.PrimaryCheckout, state.CurrentWorktree, primary, linked)
	}
	if state.CurrentBranch != "feature" {
		t.Fatalf("CurrentBranch = %q, want feature", state.CurrentBranch)
	}
	if len(state.Worktrees) != 2 {
		t.Fatalf("Worktrees = %#v, want primary and linked", state.Worktrees)
	}
	if worktree, ok := WorktreeForPath(state, filepath.Join(linked, "src", "file.go")); !ok || worktree.Branch != "feature" {
		t.Fatalf("WorktreeForPath = %#v, %v; want feature worktree", worktree, ok)
	}
}

func TestReadStateLinkedDefaultBranchWorktree(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "feature-primary")
	setReference(t, repo, plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		plumbing.NewHash(testHash),
	))
	linked := filepath.Join(t.TempDir(), "main-copy")
	addLinkedWorktree(t, primary, linked, "main")

	state, err := ReadState(linked)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.DefaultBranch != "main" || state.CurrentBranch != "main" {
		t.Fatalf("branches = %q, %q; want main, main", state.DefaultBranch, state.CurrentBranch)
	}
	if !IsDefaultBranchWorktree(state, filepath.Join(linked, "new.go")) {
		t.Fatal("linked main checkout was not recognized as the default-branch worktree")
	}
}

func TestReadStateUsesOriginHeadAndSupportsDetachedHead(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "release")
	setReference(t, repo, plumbing.NewSymbolicReference(
		plumbing.ReferenceName("refs/remotes/origin/HEAD"),
		plumbing.ReferenceName("refs/remotes/origin/release"),
	))
	setReference(t, repo, plumbing.NewHashReference(plumbing.HEAD, plumbing.NewHash(testHash)))

	state, err := ReadState(primary)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.DefaultBranch != "release" || state.CurrentBranch != "" {
		t.Fatalf("branches = %q, %q; want release and detached", state.DefaultBranch, state.CurrentBranch)
	}
}

func TestReadStateSkipsStaleLinkedWorktree(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	staleAdmin := filepath.Join(primary, ".git", "worktrees", "stale")
	writeStateFile(t, filepath.Join(staleAdmin, "gitdir"), "/missing/worktree/.git\n")

	state, err := ReadState(primary)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if len(state.Worktrees) != 1 || !state.Worktrees[0].IsPrimary {
		t.Fatalf("Worktrees = %#v; want only the healthy primary checkout", state.Worktrees)
	}
	if state.CurrentBranch != "main" || state.DefaultBranch != "main" {
		t.Fatalf("branches = %q, %q; want main, main", state.CurrentBranch, state.DefaultBranch)
	}
}

func TestWorktreeForPathResolvesSymlinkedMissingTarget(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	aliasRoot := t.TempDir()
	alias := filepath.Join(aliasRoot, "checkout")
	if err := os.Symlink(primary, alias); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	state, err := ReadState(primary)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	target := filepath.Join(alias, "missing", "new.go")
	worktree, found := WorktreeForPath(state, target)
	if !found || !worktree.IsPrimary {
		t.Fatalf("WorktreeForPath(%q) = %#v, %v; want primary checkout", target, worktree, found)
	}
}

func TestReadStateUsesPackedBranchRef(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	writeStateFile(
		t,
		filepath.Join(primary, ".git", "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted\n"+testHash+" refs/heads/main\n",
	)
	if err := os.Remove(filepath.Join(primary, ".git", "refs", "heads", "main")); err != nil {
		t.Fatalf("Remove loose main ref: %v", err)
	}

	state, err := ReadState(primary)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if state.DefaultBranch != "main" || state.CurrentBranch != "main" {
		t.Fatalf("branches = %q, %q; want packed main for both", state.DefaultBranch, state.CurrentBranch)
	}
}

func TestReadStateListsLooseAndPackedLocalBranches(t *testing.T) {
	primary, repo := initStateRepository(t)
	setBranch(t, repo, "main")
	setReference(t, repo, plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature"),
		plumbing.NewHash(testHash),
	))
	writeStateFile(
		t,
		filepath.Join(primary, ".git", "packed-refs"),
		"# pack-refs with: peeled fully-peeled sorted\n"+
			testHash+" refs/heads/main\n"+
			testHash+" refs/heads/release\n",
	)

	state, err := ReadState(primary)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	want := []string{"feature", "main", "release"}
	if !slices.Equal(state.LocalBranches, want) {
		t.Fatalf("LocalBranches = %v, want %v", state.LocalBranches, want)
	}
}

func TestFirstLineRejectsEmptyMetadataFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "HEAD")
	writeStateFile(t, path, "")

	_, err := firstLine(path)
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("firstLine() error = %v, want clear empty-file error", err)
	}
}

func TestStatePredicates(t *testing.T) {
	state := State{
		PrimaryCheckout: "/repo/main",
		DefaultBranch:   "main",
		CurrentWorktree: "/worktrees/feature",
		CurrentBranch:   "feature",
		Worktrees: []Worktree{
			{Path: "/repo/main", Branch: "main", IsPrimary: true},
			{Path: "/worktrees/feature", Branch: "feature"},
			{Path: "/worktrees/release", Branch: "release"},
		},
	}

	if !IsPrimaryCheckout(state, "/repo/main/internal/file.go") {
		t.Fatal("primary descendant was not recognized")
	}
	if IsPrimaryCheckout(state, "/repo/main-copy/file.go") {
		t.Fatal("path-prefix sibling was recognized as primary")
	}
	if !IsDefaultBranchWorktree(state, "/repo/main/file.go") {
		t.Fatal("default branch worktree was not recognized")
	}
	if !BranchCheckedOutElsewhere(state, "/worktrees/feature", "release") {
		t.Fatal("branch in another worktree was not recognized")
	}
	if BranchCheckedOutElsewhere(state, "/worktrees/feature", "feature") {
		t.Fatal("current worktree branch was recognized elsewhere")
	}
}

func initStateRepository(t *testing.T) (string, *git.Repository) {
	t.Helper()
	primary := filepath.Join(t.TempDir(), "primary")
	repo, err := git.PlainInit(primary, false)
	if err != nil {
		t.Fatalf("PlainInit: %v", err)
	}
	return primary, repo
}

func setBranch(t *testing.T, repo *git.Repository, branch string) {
	t.Helper()
	branchRef := plumbing.NewBranchReferenceName(branch)
	setReference(t, repo, plumbing.NewHashReference(branchRef, plumbing.NewHash(testHash)))
	setReference(t, repo, plumbing.NewSymbolicReference(plumbing.HEAD, branchRef))
}

func setReference(t *testing.T, repo *git.Repository, reference *plumbing.Reference) {
	t.Helper()
	if err := repo.Storer.SetReference(reference); err != nil {
		t.Fatalf("SetReference(%s): %v", reference.Name(), err)
	}
}

func addLinkedWorktree(t *testing.T, primary, linked, branch string) {
	t.Helper()
	admin := filepath.Join(primary, ".git", "worktrees", "feature")
	if err := os.MkdirAll(admin, 0o755); err != nil {
		t.Fatalf("MkdirAll admin: %v", err)
	}
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatalf("MkdirAll linked: %v", err)
	}
	writeStateFile(t, filepath.Join(linked, ".git"), "gitdir: "+admin+"\n")
	writeStateFile(t, filepath.Join(admin, "gitdir"), filepath.Join(linked, ".git")+"\n")
	writeStateFile(t, filepath.Join(admin, "commondir"), "../..\n")
	writeStateFile(t, filepath.Join(admin, "HEAD"), "ref: refs/heads/"+branch+"\n")
	writeStateFile(t, filepath.Join(primary, ".git", "refs", "heads", branch), testHash+"\n")
}

func writeStateFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile %s: %v", path, err)
	}
}
