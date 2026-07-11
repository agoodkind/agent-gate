// Package oracle contains deterministic rule classifiers used by composer.
package oracle

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"goodkind.io/agent-gate/internal/gitbranch"
)

// State describes the current repository and its registered git worktrees.
type State struct {
	PrimaryCheckout string
	DefaultBranch   string
	Worktrees       []WorktreeEntry
	CurrentWorktree string
	CurrentBranch   string
}

// WorktreeEntry is one registered git worktree.
type WorktreeEntry struct {
	Path      string
	Branch    string
	IsPrimary bool
}

// WorktreeState adapts the generic pure-Go repository state for the temporary
// rule-specific oracle. The adapter disappears when the oracle is deleted.
func WorktreeState(path string) (State, error) {
	state, err := gitbranch.ReadState(path)
	if err != nil {
		slog.Warn("read git worktree state failed", "path", path, "err", err)
		return State{}, fmt.Errorf("read git worktree state: %w", err)
	}
	worktrees := make([]WorktreeEntry, 0, len(state.Worktrees))
	for _, worktree := range state.Worktrees {
		worktrees = append(worktrees, WorktreeEntry{
			Path:      worktree.Path,
			Branch:    worktree.Branch,
			IsPrimary: worktree.IsPrimary,
		})
	}
	return State{
		PrimaryCheckout: state.PrimaryCheckout,
		DefaultBranch:   state.DefaultBranch,
		Worktrees:       worktrees,
		CurrentWorktree: state.CurrentWorktree,
		CurrentBranch:   state.CurrentBranch,
	}, nil
}

func cleanPath(value string) string {
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func branchName(value string) string {
	branch := strings.TrimSpace(value)
	for _, prefix := range []string{"refs/heads/", "refs/remotes/origin/", "origin/"} {
		if strings.HasPrefix(branch, prefix) {
			return branch[len(prefix):]
		}
	}
	return branch
}
