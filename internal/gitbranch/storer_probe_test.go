package gitbranch_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// TestStorerBypassesExtensionCheck confirms that reading HEAD/refs through a
// go-git filesystem storer skips the verifyExtensions gate that PlainOpen runs,
// so a repo with extensions.worktreeconfig can still be read.
func TestStorerBypassesExtensionCheck(t *testing.T) {
	p := os.Getenv("PROBE_PATH")
	if p == "" {
		t.Skip("PROBE_PATH not set")
	}
	gitDir := filepath.Join(p, ".git")
	storer := filesystem.NewStorage(osfs.New(gitDir), cache.NewObjectLRUDefault())
	head, err := storer.Reference(plumbing.HEAD)
	if err != nil {
		t.Fatalf("read HEAD via storer: %v", err)
	}
	t.Logf("HEAD type=%v target=%q", head.Type(), head.Target())
	branch := ""
	if head.Type() == plumbing.SymbolicReference && head.Target().IsBranch() {
		branch = head.Target().Short()
	}
	t.Logf("resolved current branch = %q", branch)

	refs, err := storer.IterReferences()
	if err != nil {
		t.Fatalf("iter refs: %v", err)
	}
	count := 0
	_ = refs.ForEach(func(r *plumbing.Reference) error {
		if r.Name().IsBranch() {
			count++
		}
		return nil
	})
	refs.Close()
	t.Logf("local branch refs readable via storer = %d", count)
}
