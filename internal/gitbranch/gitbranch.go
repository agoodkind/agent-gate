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

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// defaultBranchNames are the conventional default branch names used when a repo
// has no remote to point at origin/HEAD.
var defaultBranchNames = []string{"main", "master", "trunk"}

const originHeadRef = plumbing.ReferenceName("refs/remotes/origin/HEAD")

// OnDefaultBranch reports whether the git repo or worktree containing path has
// HEAD on its default branch. resolved is false when no repo is found or the
// branch state cannot be read; a detached HEAD resolves true with match false.
// Callers that block should treat resolved==false and detached as "do not
// block" (fail open).
func OnDefaultBranch(path string) (match bool, resolved bool) {
	start := nearestExistingDir(path)
	if start == "" {
		return false, false
	}
	repo, err := git.PlainOpenWithOptions(start, &git.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return false, false
	}
	head, err := repo.Head()
	if err != nil {
		return false, false
	}
	if !head.Name().IsBranch() {
		return false, true
	}
	defaultBranch := resolveDefaultBranch(repo)
	if defaultBranch == "" {
		// The default branch could not be determined, so the branch state is
		// unresolved. Report resolved=false to preserve the fail-open contract
		// rather than claiming a resolved non-match.
		return false, false
	}
	return head.Name().Short() == defaultBranch, true
}

// resolveDefaultBranch returns the repo's default branch name: the target of
// origin/HEAD when a remote records one, else the first conventional default
// that has a local branch ref, else empty.
func resolveDefaultBranch(repo *git.Repository) string {
	if ref, err := repo.Reference(originHeadRef, false); err == nil {
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
		if _, err := repo.Reference(plumbing.NewBranchReferenceName(name), false); err == nil {
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
