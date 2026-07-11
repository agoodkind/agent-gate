package gitbranch

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

// State describes one repository and its registered worktrees.
type State struct {
	PrimaryCheckout string
	DefaultBranch   string
	CurrentWorktree string
	CurrentBranch   string
	LocalBranches   []string
	Worktrees       []Worktree
}

// Worktree describes one checkout registered with a repository.
type Worktree struct {
	Path      string
	Branch    string
	IsPrimary bool
}

// ReadState reads repository and worktree state for path without invoking git.
// It reads references through a go-git filesystem storer rather than opening the
// repository with PlainOpen, because PlainOpen runs an extension check that
// rejects any repository with the (harmless) extensions.worktreeconfig setting
// that git enables for linked worktrees. Reading the storer directly still uses
// go-git for HEAD, branches, packed-refs, and origin/HEAD, but skips that check.
func ReadState(path string) (State, error) {
	start := nearestExistingDir(path)
	if start == "" {
		return State{}, errors.New("no existing ancestor")
	}
	currentWorktree, gitDir, err := findWorktreeRoot(start)
	if err != nil {
		return State{}, err
	}
	commonDir, err := resolveCommonDir(gitDir)
	if err != nil {
		return State{}, err
	}
	primaryCheckout := cleanStatePath(filepath.Dir(commonDir))
	worktrees, err := readWorktrees(primaryCheckout, commonDir)
	if err != nil {
		return State{}, err
	}
	storer := openStorer(gitDir, commonDir)
	localBranches, err := readLocalBranches(storer)
	if err != nil {
		return State{}, err
	}
	return State{
		PrimaryCheckout: primaryCheckout,
		DefaultBranch:   resolveDefaultBranch(storer),
		CurrentWorktree: currentWorktree,
		CurrentBranch:   headBranch(storer),
		LocalBranches:   localBranches,
		Worktrees:       worktrees,
	}, nil
}

// openStorer builds a go-git filesystem storer for a worktree's git directory,
// wiring in the shared common directory so a linked worktree resolves its
// branches and packed-refs from the primary repository. It never runs the
// PlainOpen extension check.
func openStorer(gitDir string, commonDir string) storage.Storer {
	dotFs := osfs.New(gitDir)
	if commonDir != "" && cleanStatePath(commonDir) != cleanStatePath(gitDir) {
		repoFs := dotgit.NewRepositoryFilesystem(dotFs, osfs.New(commonDir))
		return filesystem.NewStorage(repoFs, cache.NewObjectLRUDefault())
	}
	return filesystem.NewStorage(dotFs, cache.NewObjectLRUDefault())
}

// headBranch returns the branch HEAD points at, or empty when HEAD is detached,
// unreadable, or not a branch.
func headBranch(storer storage.Storer) string {
	ref, err := storer.Reference(plumbing.HEAD)
	if err != nil {
		return ""
	}
	if ref.Type() == plumbing.SymbolicReference && ref.Target().IsBranch() {
		return ref.Target().Short()
	}
	return ""
}

// WorktreeForPath returns the most specific worktree containing path.
func WorktreeForPath(state State, path string) (Worktree, bool) {
	candidate := cleanStatePath(path)
	var best Worktree
	found := false
	for _, worktree := range state.Worktrees {
		root := cleanStatePath(worktree.Path)
		if root == "" || candidate != root && !strings.HasPrefix(candidate, root+string(filepath.Separator)) {
			continue
		}
		if !found || len(best.Path) < len(worktree.Path) {
			best = worktree
			found = true
		}
	}
	return best, found
}

// IsPrimaryCheckout reports whether path is inside the primary checkout.
func IsPrimaryCheckout(state State, path string) bool {
	worktree, found := WorktreeForPath(state, path)
	return found && worktree.IsPrimary
}

// IsDefaultBranchWorktree reports whether path is inside a default-branch checkout.
func IsDefaultBranchWorktree(state State, path string) bool {
	worktree, found := WorktreeForPath(state, path)
	return found && state.DefaultBranch != "" && worktree.Branch == state.DefaultBranch
}

// BranchCheckedOutElsewhere reports whether branch belongs to another worktree.
func BranchCheckedOutElsewhere(state State, currentPath, branch string) bool {
	current, _ := WorktreeForPath(state, currentPath)
	for _, worktree := range state.Worktrees {
		if worktree.Branch == branch && cleanStatePath(worktree.Path) != cleanStatePath(current.Path) {
			return true
		}
	}
	return false
}

func findWorktreeRoot(start string) (string, string, error) {
	current := filepath.Clean(start)
	for {
		dotGit := filepath.Join(current, ".git")
		info, err := os.Stat(dotGit)
		if err == nil {
			if info.IsDir() {
				return cleanStatePath(current), dotGit, nil
			}
			gitDir, readErr := readGitDirFile(dotGit)
			if readErr != nil {
				return "", "", readErr
			}
			return cleanStatePath(current), gitDir, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", errors.New("repository metadata not found")
		}
		current = parent
	}
}

func readGitDirFile(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		slog.Warn("read gitdir file failed", "path", path, "err", err)
		return "", fmt.Errorf("read gitdir file: %w", err)
	}
	value, found := strings.CutPrefix(strings.TrimSpace(string(content)), "gitdir:")
	if !found {
		return "", errors.New("invalid gitdir file")
	}
	value = strings.TrimSpace(value)
	if !filepath.IsAbs(value) {
		value = filepath.Join(filepath.Dir(path), value)
	}
	return filepath.Clean(value), nil
}

func resolveCommonDir(gitDir string) (string, error) {
	content, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(gitDir), nil
	}
	if err != nil {
		slog.Warn("read git common directory failed", "git_dir", gitDir, "err", err)
		return "", fmt.Errorf("read commondir: %w", err)
	}
	commonDir := strings.TrimSpace(string(content))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(gitDir, commonDir)
	}
	return filepath.Clean(commonDir), nil
}

func readWorktrees(primaryCheckout, commonDir string) ([]Worktree, error) {
	primaryBranch := headBranch(openStorer(commonDir, commonDir))
	worktrees := []Worktree{{Path: cleanStatePath(primaryCheckout), Branch: primaryBranch, IsPrimary: true}}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if errors.Is(err, os.ErrNotExist) {
		return worktrees, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read worktrees: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		adminDir := filepath.Join(commonDir, "worktrees", entry.Name())
		gitDirPath, readErr := firstLine(filepath.Join(adminDir, "gitdir"))
		if readErr != nil {
			slog.Warn("skip unreadable linked worktree metadata", "admin_dir", adminDir, "err", readErr)
			continue
		}
		if !filepath.IsAbs(gitDirPath) {
			gitDirPath = filepath.Join(adminDir, gitDirPath)
		}
		linkedPath := filepath.Dir(gitDirPath)
		if _, statErr := os.Stat(linkedPath); errors.Is(statErr, os.ErrNotExist) {
			continue
		} else if statErr != nil {
			slog.Warn("stat linked git worktree failed", "path", linkedPath, "err", statErr)
			return nil, fmt.Errorf("stat linked worktree: %w", statErr)
		}
		branch, branchErr := readWorktreeBranch(adminDir)
		if branchErr != nil {
			slog.Warn("skip unreadable linked worktree branch", "admin_dir", adminDir, "err", branchErr)
			continue
		}
		worktrees = append(worktrees, Worktree{
			Path:      cleanStatePath(linkedPath),
			Branch:    branch,
			IsPrimary: false,
		})
	}
	return worktrees, nil
}

func readWorktreeBranch(adminDir string) (string, error) {
	head, err := firstLine(filepath.Join(adminDir, "HEAD"))
	if err != nil {
		return "", err
	}
	branch, found := strings.CutPrefix(head, "ref: refs/heads/")
	if !found {
		return "", nil
	}
	return branch, nil
}

func readLocalBranches(storer storage.Storer) ([]string, error) {
	references, err := storer.IterReferences()
	if err != nil {
		slog.Warn("read local git branches failed", "err", err)
		return nil, fmt.Errorf("read local branches: %w", err)
	}
	defer references.Close()
	branchNames := make(map[string]struct{})
	if err := references.ForEach(func(reference *plumbing.Reference) error {
		if reference.Name().IsBranch() {
			branchNames[reference.Name().Short()] = struct{}{}
		}
		return nil
	}); err != nil {
		slog.Warn("iterate local git branches failed", "err", err)
		return nil, fmt.Errorf("iterate local branches: %w", err)
	}
	result := make([]string, 0, len(branchNames))
	for branchName := range branchNames {
		result = append(result, branchName)
	}
	slices.Sort(result)
	return result, nil
}

func firstLine(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		slog.Warn("open git metadata file failed", "path", path, "err", err)
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if scanErr := scanner.Err(); scanErr != nil {
			slog.Warn("read git metadata file failed", "path", path, "err", scanErr)
			return "", fmt.Errorf("read %s: %w", path, scanErr)
		}
		return "", fmt.Errorf("read %s: metadata file is empty", path)
	}
	return strings.TrimSpace(scanner.Text()), nil
}

func cleanStatePath(path string) string {
	if path == "" {
		return ""
	}
	cleaned, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	current := cleaned
	remaining := make([]string, 0)
	for {
		if resolved, resolveErr := filepath.EvalSymlinks(current); resolveErr == nil {
			parts := append([]string{resolved}, remaining...)
			return filepath.Join(parts...)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(cleaned)
		}
		remaining = append([]string{filepath.Base(current)}, remaining...)
		current = parent
	}
}
