package composer

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/internal/oracle"
	"goodkind.io/clyde/api/contextpb"
)

type fakeConversationClient struct {
	reply *contextpb.GetRecentTurnsReply
	err   error
	req   *contextpb.GetRecentTurnsRequest
}

func (c *fakeConversationClient) GetRecentTurns(
	_ context.Context,
	req *contextpb.GetRecentTurnsRequest,
	_ ...grpc.CallOption,
) (*contextpb.GetRecentTurnsReply, error) {
	c.req = req
	if c.err != nil {
		return nil, c.err
	}
	return c.reply, nil
}

func TestResolveFillsRequestedContext(t *testing.T) {
	const cwd = "/repo"
	client := &fakeConversationClient{
		reply: &contextpb.GetRecentTurnsReply{Turns: []*contextpb.Turn{
			{Role: "user", Text: "please inspect"},
			{Role: "assistant", Text: "looking now"},
		}},
	}
	deps := Deps{
		Clyde: client,
		IndexedRoots: func(context.Context) ([]string, error) {
			return []string{"/repo", "/other"}, nil
		},
		WorktreeState: func(string) (oracle.State, error) {
			return oracle.State{}, errors.New("not requested")
		},
	}

	got := Resolve([]string{"indexed_roots", "conversation"}, cwd, deps)
	if got.GetCwd() != cwd {
		t.Fatalf("cwd = %q, want %q", got.GetCwd(), cwd)
	}
	if len(got.GetIndexedRoots()) != 2 || got.GetIndexedRoots()[0] != "/repo" {
		t.Fatalf("IndexedRoots = %v", got.GetIndexedRoots())
	}
	if got.GetCwdIndexed() != "yes" {
		t.Fatalf("CwdIndexed = %q, want yes", got.GetCwdIndexed())
	}
	if len(got.GetConversation()) != 2 || got.GetConversation()[1].GetText() != "looking now" {
		t.Fatalf("Conversation = %#v", got.GetConversation())
	}
	if got.GetWorktree() != nil {
		t.Fatalf("Worktree = %#v, want nil", got.GetWorktree())
	}
	if client.req.GetWorkspaceRef() != cwd {
		t.Fatalf("WorkspaceRef = %q, want %q", client.req.GetWorkspaceRef(), cwd)
	}
}

func TestResolveIgnoresUnavailableProviders(t *testing.T) {
	deps := Deps{
		Clyde: &fakeConversationClient{err: errors.New("unavailable")},
		IndexedRoots: func(context.Context) ([]string, error) {
			return nil, errors.New("unavailable")
		},
		WorktreeState: func(string) (oracle.State, error) {
			return oracle.State{}, errors.New("not a git repo")
		},
	}

	got := Resolve([]string{"conversation", "indexed_roots", "worktree"}, "/repo", deps)
	if len(got.GetConversation()) != 0 {
		t.Fatalf("Conversation = %#v, want empty", got.GetConversation())
	}
	if len(got.GetIndexedRoots()) != 0 {
		t.Fatalf("IndexedRoots = %v, want empty", got.GetIndexedRoots())
	}
	if got.GetWorktree() != nil {
		t.Fatalf("Worktree = %#v, want nil", got.GetWorktree())
	}
}

func TestResolveMapsWorktreeState(t *testing.T) {
	deps := Deps{
		WorktreeState: func(string) (oracle.State, error) {
			return oracle.State{
				PrimaryCheckout: "/repo/main",
				DefaultBranch:   "main",
				Worktrees: []oracle.WorktreeEntry{
					{Path: "/repo/main", Branch: "main", IsPrimary: true},
					{Path: "/repo-feature", Branch: "feature"},
				},
				CurrentWorktree: "/repo-feature",
				CurrentBranch:   "feature",
			}, nil
		},
	}

	got := Resolve([]string{"worktree"}, "/repo-feature", deps)
	worktree := got.GetWorktree()
	if worktree == nil {
		t.Fatal("Worktree = nil, want state")
	}
	if worktree.GetPrimaryCheckout() != "/repo/main" || worktree.GetCurrentBranch() != "feature" {
		t.Fatalf("Worktree = %#v", worktree)
	}
	if len(worktree.GetWorktrees()) != 2 || !worktree.GetWorktrees()[0].GetIsPrimary() {
		t.Fatalf("Worktrees = %#v", worktree.GetWorktrees())
	}
}
