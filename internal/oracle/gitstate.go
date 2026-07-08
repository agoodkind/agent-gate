// Package oracle contains deterministic rule classifiers used by composer.
package oracle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
)

// State describes the current repository and its registered git worktrees.
type State struct {
	PrimaryCheckout string
	DefaultBranch   string
	Worktrees       []WorktreeEntry
	CurrentWorktree string
	CurrentBranch   string
}

// WorktreeEntry is one path from git worktree list --porcelain.
type WorktreeEntry struct {
	Path      string
	Branch    string
	IsPrimary bool
}

// WorktreeState reads git worktree and branch state for the repository at cwd.
func WorktreeState(cwd string) (State, error) {
	slog.Debug("loading git worktree state", "cwd", cwd)
	worktreeOutput, err := runGitOutput(cwd, gitOutputWorktreeList, "")
	if err != nil {
		slog.Warn("git worktree list failed", "cwd", cwd, "err", err)
		return State{}, fmt.Errorf("list git worktrees: %w", err)
	}
	worktrees := parseWorktreeList(worktreeOutput)
	if len(worktrees) == 0 {
		return State{}, errors.New("git worktree list returned no worktrees")
	}

	defaultBranch := readDefaultBranch(cwd)
	currentWorktree, err := runGitOutput(cwd, gitOutputRevParseToplevel, "")
	if err != nil {
		slog.Warn("git rev-parse toplevel failed", "cwd", cwd, "err", err)
		return State{}, fmt.Errorf("read git toplevel: %w", err)
	}
	currentBranch := readCurrentBranch(cwd)
	state := State{
		PrimaryCheckout: worktrees[0].Path,
		DefaultBranch:   defaultBranch,
		Worktrees:       worktrees,
		CurrentWorktree: cleanPath(currentWorktree),
		CurrentBranch:   branchName(currentBranch),
	}
	slog.Debug(
		"loaded git worktree state",
		"cwd", cwd,
		"primary_checkout", state.PrimaryCheckout,
		"default_branch", state.DefaultBranch,
		"current_worktree", state.CurrentWorktree,
		"current_branch", state.CurrentBranch,
		"worktree_count", len(state.Worktrees),
	)
	return state, nil
}

func runGitOutput(cwd string, kind gitOutputCommand, branch string) (string, error) {
	slog.Debug("running git command", "cwd", cwd, "command", kind.String(), "branch", branch)
	command, err := gitCommandContext(context.Background(), cwd, kind, branch)
	if err != nil {
		slog.Warn("build git command failed", "cwd", cwd, "command", kind.String(), "branch", branch, "err", err)
		return "", fmt.Errorf("build git command %s: %w", kind.String(), err)
	}
	output, err := command.Output()
	if err != nil {
		slog.Warn("git command failed", "cwd", cwd, "command", kind.String(), "branch", branch, "err", err)
		return "", fmt.Errorf("git %s: %w", kind.String(), err)
	}
	return strings.TrimSpace(string(output)), nil
}

type gitOutputCommand int

const (
	gitOutputWorktreeList gitOutputCommand = iota
	gitOutputSymbolicOriginHead
	gitOutputRevParseToplevel
	gitOutputRevParseVerifyBranch
	gitOutputRevParseAbbrevHead
)

func (kind gitOutputCommand) String() string {
	switch kind {
	case gitOutputWorktreeList:
		return "worktree list --porcelain"
	case gitOutputSymbolicOriginHead:
		return "symbolic-ref refs/remotes/origin/HEAD"
	case gitOutputRevParseToplevel:
		return "rev-parse --show-toplevel"
	case gitOutputRevParseVerifyBranch:
		return "rev-parse --verify refs/heads/<branch>"
	case gitOutputRevParseAbbrevHead:
		return "rev-parse --abbrev-ref HEAD"
	default:
		return "unknown"
	}
}

func gitCommandContext(ctx context.Context, cwd string, kind gitOutputCommand, branch string) (*exec.Cmd, error) {
	slog.DebugContext(ctx, "building git command", "cwd", cwd, "command", kind.String(), "branch", branch)
	switch kind {
	case gitOutputWorktreeList:
		return exec.CommandContext(ctx, "git", "-C", cwd, "worktree", "list", "--porcelain"), nil
	case gitOutputSymbolicOriginHead:
		return exec.CommandContext(ctx, "git", "-C", cwd, "symbolic-ref", "refs/remotes/origin/HEAD"), nil
	case gitOutputRevParseToplevel:
		return exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--show-toplevel"), nil
	case gitOutputRevParseVerifyBranch:
		if !defaultBranchCandidate(branch) {
			return nil, fmt.Errorf("unsupported default branch candidate %q", branch)
		}
		return exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--verify", "refs/heads/"+branch), nil
	case gitOutputRevParseAbbrevHead:
		return exec.CommandContext(ctx, "git", "-C", cwd, "rev-parse", "--abbrev-ref", "HEAD"), nil
	default:
		return nil, fmt.Errorf("unknown git output command %d", kind)
	}
}

func defaultBranchCandidate(branch string) bool {
	return branch == "main" || branch == "master" || branch == "trunk"
}

func readDefaultBranch(cwd string) string {
	output, err := runGitOutput(cwd, gitOutputSymbolicOriginHead, "")
	if err == nil && output != "" {
		return branchName(output)
	}
	for _, candidate := range []string{"main", "master", "trunk"} {
		if _, branchErr := runGitOutput(cwd, gitOutputRevParseVerifyBranch, candidate); branchErr == nil {
			return candidate
		}
	}
	return ""
}

func readCurrentBranch(cwd string) string {
	output, err := runGitOutput(cwd, gitOutputRevParseAbbrevHead, "")
	if err != nil || output == "HEAD" {
		return ""
	}
	return output
}

func parseWorktreeList(output string) []WorktreeEntry {
	blocks := bytes.Split([]byte(output), []byte("\n\n"))
	worktrees := make([]WorktreeEntry, 0, len(blocks))
	for index, block := range blocks {
		entry := parseWorktreeBlock(string(block))
		if entry.Path == "" {
			continue
		}
		entry.IsPrimary = index == 0
		worktrees = append(worktrees, entry)
	}
	return worktrees
}

func parseWorktreeBlock(block string) WorktreeEntry {
	var entry WorktreeEntry
	for line := range strings.SplitSeq(block, "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		switch worktreePorcelainKey(key) {
		case worktreePorcelainKeyWorktree:
			entry.Path = cleanPath(value)
		case worktreePorcelainKeyBranch:
			entry.Branch = branchName(value)
		}
	}
	return entry
}

type worktreePorcelainKey string

const (
	worktreePorcelainKeyWorktree worktreePorcelainKey = "worktree"
	worktreePorcelainKeyBranch   worktreePorcelainKey = "branch"
)

func cleanPath(value string) string {
	if value == "" {
		return ""
	}
	return filepath.Clean(value)
}

func branchName(value string) string {
	branch := strings.TrimSpace(value)
	for _, prefix := range []string{
		"refs/heads/",
		"refs/remotes/origin/",
		"origin/",
	} {
		if strings.HasPrefix(branch, prefix) {
			return branch[len(prefix):]
		}
	}
	return branch
}
