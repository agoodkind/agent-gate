package gitbranch_test

import (
	"os"
	"testing"

	"goodkind.io/agent-gate/internal/gitbranch"
)

func TestPrimaryCheckoutProbe(t *testing.T) {
	p := os.Getenv("PROBE_PATH")
	if p == "" {
		t.Skip("PROBE_PATH not set")
	}
	st, err := gitbranch.ReadState(p)
	t.Logf("ReadState err=%v primaryCheckout=%q worktrees=%d", err, st.PrimaryCheckout, len(st.Worktrees))
	for _, w := range st.Worktrees {
		t.Logf("  worktree path=%q branch=%q isPrimary=%v", w.Path, w.Branch, w.IsPrimary)
	}
	t.Logf("IsPrimaryCheckout(%q) = %v", p, gitbranch.IsPrimaryCheckout(st, p))
}
