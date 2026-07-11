package rules

import "testing"

func TestBranchTargetsForCurrentBranchRename(t *testing.T) {
	targets := branchTargetsForMode(branchMoveRename, []string{"renamed"})
	if len(targets) != 1 || targets[0] != gitCurrentBranchTarget {
		t.Fatalf("branch targets = %v, want current branch target", targets)
	}
}
