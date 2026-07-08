package composer

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/internal/oracle"
	"goodkind.io/clyde/api/contextpb"
	"goodkind.io/lm-review/api/judgepb"
)

const contextProviderTimeout = 750 * time.Millisecond

// ConversationClient is the clyde conversation-context client surface.
type ConversationClient interface {
	GetRecentTurns(context.Context, *contextpb.GetRecentTurnsRequest, ...grpc.CallOption) (*contextpb.GetRecentTurnsReply, error)
}

// IndexedRootsSource returns canonical roots currently indexed by lm-semantic-search.
type IndexedRootsSource func(context.Context) ([]string, error)

// WorktreeStateFunc resolves git worktree state for a working directory.
type WorktreeStateFunc func(string) (oracle.State, error)

// Deps holds optional context providers. Every provider may be nil.
type Deps struct {
	Clyde         ConversationClient
	IndexedRoots  IndexedRootsSource
	WorktreeState WorktreeStateFunc
}

// Resolve builds the judge context requested by a rule set. Provider failures
// leave that part of the context empty.
func Resolve(required []string, cwd string, deps Deps) *judgepb.RuleContext {
	ruleContext := &judgepb.RuleContext{Cwd: cwd}
	if slices.Contains(required, "indexed_roots") {
		resolveIndexedRoots(ruleContext, cwd, deps.IndexedRoots)
	}
	if slices.Contains(required, "conversation") {
		resolveConversation(ruleContext, cwd, deps.Clyde)
	}
	if slices.Contains(required, "worktree") {
		resolveWorktree(ruleContext, cwd, deps.WorktreeState)
	}
	return ruleContext
}

func resolveIndexedRoots(
	ruleContext *judgepb.RuleContext,
	cwd string,
	source IndexedRootsSource,
) {
	if source == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextProviderTimeout)
	defer cancel()
	roots, err := source(ctx)
	if err != nil {
		return
	}
	ruleContext.IndexedRoots = append([]string(nil), roots...)
	if cwdIsIndexed(cwd, roots) {
		ruleContext.CwdIndexed = "yes"
		return
	}
	ruleContext.CwdIndexed = "no"
}

func resolveConversation(
	ruleContext *judgepb.RuleContext,
	cwd string,
	client ConversationClient,
) {
	if client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), contextProviderTimeout)
	defer cancel()
	reply, err := client.GetRecentTurns(ctx, &contextpb.GetRecentTurnsRequest{
		WorkspaceRef:    cwd,
		SessionRef:      "",
		TurnBudget:      4,
		MaxCharsPerTurn: 280,
	})
	if err != nil || reply == nil {
		return
	}
	for _, turn := range reply.GetTurns() {
		if turn == nil {
			continue
		}
		ruleContext.Conversation = append(ruleContext.Conversation, &judgepb.ConversationTurn{
			Role: turn.GetRole(),
			Text: turn.GetText(),
		})
	}
}

func resolveWorktree(
	ruleContext *judgepb.RuleContext,
	cwd string,
	stateFunc WorktreeStateFunc,
) {
	if stateFunc == nil {
		return
	}
	state, err := stateFunc(cwd)
	if err != nil {
		return
	}
	ruleContext.Worktree = worktreeStateProto(state)
}

func worktreeStateProto(state oracle.State) *judgepb.WorktreeState {
	out := &judgepb.WorktreeState{
		PrimaryCheckout: state.PrimaryCheckout,
		DefaultBranch:   state.DefaultBranch,
		CurrentWorktree: state.CurrentWorktree,
		CurrentBranch:   state.CurrentBranch,
	}
	for _, worktree := range state.Worktrees {
		out.Worktrees = append(out.Worktrees, &judgepb.Worktree{
			Path:      worktree.Path,
			Branch:    worktree.Branch,
			IsPrimary: worktree.IsPrimary,
		})
	}
	return out
}

func cwdIsIndexed(cwd string, roots []string) bool {
	cleanCwd := filepath.Clean(cwd)
	for _, root := range roots {
		cleanRoot := filepath.Clean(root)
		if cleanCwd == cleanRoot || isPathChild(cleanCwd, cleanRoot) {
			return true
		}
	}
	return false
}

func isPathChild(candidate string, root string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !isParentRelative(rel)
}

func isParentRelative(path string) bool {
	return path == ".." || len(path) > 3 && path[:3] == "../"
}
