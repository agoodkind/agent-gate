package rules_test

import (
	"context"
	"sync"
	"testing"

	"goodkind.io/agent-gate/internal/composer"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

type recordingComposerDecider struct {
	mu        sync.Mutex
	calls     int
	ruleSetID string
	command   string
	cwd       string
	verdict   composer.Verdict
}

func (d *recordingComposerDecider) Decide(ruleSetID string, command string, cwd string) composer.Verdict {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	d.ruleSetID = ruleSetID
	d.command = command
	d.cwd = cwd
	return d.verdict
}

func (d *recordingComposerDecider) Calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

func (d *recordingComposerDecider) LastCall() (string, string, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ruleSetID, d.command, d.cwd
}

func TestComposerConditionBlocksAndAllowsFromDecider(t *testing.T) {
	rule := loadComposerRule(t)
	fields := rules.FieldSet{
		CWD:              "/repo",
		ToolInputCommand: "grep -rn TODO .",
	}

	blockingDecider := &recordingComposerDecider{verdict: composer.Block}
	blockingCtx := rules.WithComposerDecider(context.Background(), blockingDecider)
	blocked := rules.EvaluateAll(blockingCtx, "codex", "PreToolUse", fields, []config.Rule{rule}, nil)
	if len(blocked) == 0 {
		t.Fatal("composer block verdict should produce a violation")
	}
	ruleSetID, command, cwd := blockingDecider.LastCall()
	if ruleSetID != "search-guard" || command != fields.ToolInputCommand || cwd != fields.CWD {
		t.Fatalf("composer call = %q %q %q", ruleSetID, command, cwd)
	}

	allowingDecider := &recordingComposerDecider{verdict: composer.Allow}
	allowingCtx := rules.WithComposerDecider(context.Background(), allowingDecider)
	allowed := rules.EvaluateAll(allowingCtx, "codex", "PreToolUse", fields, []config.Rule{rule}, nil)
	if len(allowed) != 0 {
		t.Fatalf("composer allow verdict should allow, got %d violations", len(allowed))
	}
}

func TestComposerConditionKeepsRegexPrefilterCheap(t *testing.T) {
	rule := loadComposerRule(t)
	decider := &recordingComposerDecider{verdict: composer.Block}
	ctx := rules.WithComposerDecider(context.Background(), decider)

	violations := rules.EvaluateAll(ctx, "codex", "PreToolUse", rules.FieldSet{
		CWD:              "/repo",
		ToolInputCommand: "echo hello",
	}, []config.Rule{rule}, nil)
	if len(violations) != 0 {
		t.Fatalf("regex mismatch should allow, got %d violations", len(violations))
	}
	if decider.Calls() != 0 {
		t.Fatalf("composer decider called %d times despite regex prefilter mismatch", decider.Calls())
	}
}

func loadComposerRule(t *testing.T) config.Rule {
	t.Helper()
	cfg := loadTOML(t, `
[[rules]]
name = "grep-code-fail-closed"
events = ["PreToolUse"]
action = "block"
violation_message = "Code search against indexed roots must go through semantic search."

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "composer"
rule_set_id = "search-guard"
`)
	return cfg.Rules[0]
}
