package rules

import (
	"context"
	"errors"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/gitbranch"
)

// TestResolveJudgeContextFieldSelector confirms a field-path key resolves through
// the same FieldSet accessor the deterministic conditions use.
func TestResolveJudgeContextFieldSelector(t *testing.T) {
	fields := FieldSet{
		ToolName:         "Bash",
		CWD:              "/repo",
		ToolInputCommand: "echo hi > /tmp/out.txt",
	}
	facts := resolveJudgeContext(context.Background(), fields, []string{"cmd_write_targets"})
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want one", facts)
	}
	if facts[0].Label != "cmd_write_targets" || !strings.Contains(facts[0].Value, "/tmp/out.txt") {
		t.Fatalf("fact = %+v, want cmd_write_targets containing /tmp/out.txt", facts[0])
	}
}

// TestResolveJudgeContextUnknownKeyDropped confirms an unknown key resolves to no
// fact rather than a fabricated one. Config validation rejects unknown keys, so
// this only guards the resolver against a key that slips through.
func TestResolveJudgeContextUnknownKeyDropped(t *testing.T) {
	facts := resolveJudgeContext(context.Background(), FieldSet{}, []string{"not_a_field"})
	if len(facts) != 0 {
		t.Fatalf("facts = %+v, want none", facts)
	}
}

// TestResolveCheckoutStatusOutsideRepo confirms checkout_status labels a directory
// whose git state cannot be read as outside a repository, and reports no primary
// write target. The reader is stubbed to error, so the test does no disk I/O.
func TestResolveCheckoutStatusOutsideRepo(t *testing.T) {
	stub := func(string) (gitbranch.State, error) {
		return gitbranch.State{}, errors.New("no repo")
	}
	ctx := WithGitStateReader(context.Background(), stub)
	fields := FieldSet{
		ToolName:         "Bash",
		CWD:              "/tmp/work",
		ToolInputCommand: "echo hi > /tmp/out.txt",
	}
	facts := resolveJudgeContext(ctx, fields, []string{"checkout_status"})

	var gotDir, gotWrites string
	for _, fact := range facts {
		if fact.Label == "checkout_status effective_cwd" {
			gotDir = fact.Value
		}
		if fact.Label == "write targets under the primary checkout" {
			gotWrites = fact.Value
		}
	}
	if !strings.Contains(gotDir, "not in a git repository") {
		t.Fatalf("effective_cwd label = %q, want not-in-a-git-repository", gotDir)
	}
	if gotWrites != "none" {
		t.Fatalf("write-targets label = %q, want none", gotWrites)
	}
}

// TestBuildJudgeInputRendersFacts confirms the resolved facts appear under the
// heading, and that a checkout_status fact suppresses the raw working-directory
// lines so the judge decides from the labeled fact.
func TestBuildJudgeInputRendersFacts(t *testing.T) {
	fields := FieldSet{CWD: "/repo", ToolName: "Bash", ToolInputCommand: "git status"}
	facts := []judgeContextFact{
		{Label: "checkout_status effective_cwd", Value: "a linked worktree, not the primary checkout", FromCheckoutStatus: true},
		{Label: "write targets under the primary checkout", Value: "none", FromCheckoutStatus: true},
	}
	out := buildJudgeInput(fields, "", facts)
	if !strings.Contains(out, "resolved facts:") {
		t.Fatalf("output has no resolved-facts heading: %q", out)
	}
	if !strings.Contains(out, "checkout_status effective_cwd: a linked worktree, not the primary checkout") {
		t.Fatalf("output missing checkout label: %q", out)
	}
	if !strings.Contains(out, "write targets under the primary checkout: none") {
		t.Fatalf("output missing write-target fact: %q", out)
	}
	if strings.Contains(out, "chat working directory:") {
		t.Fatalf("checkout_status present but raw working-directory line not suppressed: %q", out)
	}
}

// TestBuildJudgeInputKeepsCwdWithoutCheckoutStatus confirms a rule that requests
// only a field-path fact still sees the raw working-directory lines, so the
// suppression is scoped to rules that opt into checkout_status.
func TestBuildJudgeInputKeepsCwdWithoutCheckoutStatus(t *testing.T) {
	fields := FieldSet{CWD: "/repo", ToolName: "Bash", ToolInputCommand: "git status"}
	facts := []judgeContextFact{
		{Label: "cmd_write_targets", Value: "/tmp/out.txt", FromCheckoutStatus: false},
	}
	out := buildJudgeInput(fields, "", facts)
	if !strings.Contains(out, "chat working directory: /repo") {
		t.Fatalf("no checkout_status fact but working-directory line was suppressed: %q", out)
	}
	if !strings.Contains(out, "cmd_write_targets: /tmp/out.txt") {
		t.Fatalf("output missing field-path fact: %q", out)
	}
}
