// Package gitbranch reports whether the git repository containing a path
// currently has HEAD on its default branch. It uses the pure-Go
// github.com/go-git/go-git/v5 implementation rather than shelling out to the git
// binary or parsing .git files by hand, so worktrees, packed-refs, and
// origin/HEAD are all handled by the library.
package gitbranch

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage"
)

// defaultBranchNames are the conventional default branch names used when a repo
// has no remote to point at origin/HEAD.
var defaultBranchNames = []string{"main", "master", "trunk"}

const originHeadRef = plumbing.ReferenceName("refs/remotes/origin/HEAD")

// resolveDefaultBranch returns the repo's default branch name: the target of
// origin/HEAD when a remote records one, else the first conventional default
// that has a local branch ref, else empty.
func resolveDefaultBranch(storer storage.Storer) string {
	if ref, err := storer.Reference(originHeadRef); err == nil {
		if ref.Type() == plumbing.SymbolicReference {
			// Only accept the expected refs/remotes/origin/<branch> shape. An
			// origin/HEAD that targets some other refname is not a usable default
			// name, so fall through to the conventional-default fallback rather
			// than returning a bogus prefixed string that never matches HEAD.
			if name, ok := strings.CutPrefix(ref.Target().String(), "refs/remotes/origin/"); ok && name != "" {
				return name
			}
		}
	}
	for _, name := range defaultBranchNames {
		if _, err := storer.Reference(plumbing.NewBranchReferenceName(name)); err == nil {
			return name
		}
	}
	return ""
}

// nearestExistingDir returns path when it is an existing directory, the parent
// of path when path exists as a file, or the nearest existing ancestor
// directory when path does not exist yet (an edit target may be a new file).
func nearestExistingDir(path string) string {
	if path == "" {
		return ""
	}
	current := path
	for {
		info, err := os.Stat(current)
		if err == nil {
			if info.IsDir() {
				return current
			}
			return filepath.Dir(current)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}
