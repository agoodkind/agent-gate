package rules_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

type runnerResponse struct {
	res execconcern.RunResult
	err error
}

// countingRunner records how often the validator forked and returns scripted
// responses in order (the last repeats), so tests assert fork counts without
// spawning a real process.
type countingRunner struct {
	mu        sync.Mutex
	calls     int
	responses []runnerResponse
}

func newCountingRunner(res execconcern.RunResult, err error) *countingRunner {
	return &countingRunner{responses: []runnerResponse{{res: res, err: err}}}
}

func (r *countingRunner) Run(_ context.Context, _ []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	idx := r.calls
	r.calls++
	if idx >= len(r.responses) {
		idx = len(r.responses) - 1
	}
	resp := r.responses[idx]
	return resp.res, resp.err
}

func (r *countingRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func loadExecRule(t *testing.T, body string) config.Rule {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg.Rules[0]
}

func execRuleTOML(extraExec string) string {
	return fmt.Sprintf(`
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
%s
`, extraExec)
}

func evalRule(runner execconcern.Runner, rule config.Rule, payload map[string]any) []rules.Violation {
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	return rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
}

func TestExecGateShortCircuitsWhenCheaperConditionFails(t *testing.T) {
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "ls -la"},
	})

	if len(violations) != 0 {
		t.Fatalf("expected allow when regex does not match, got %d violations", len(violations))
	}
	if runner.Calls() != 0 {
		t.Fatalf("validator must not fork when a cheaper condition fails, forked %d times", runner.Calls())
	}
}

func TestExecGateForksOncePerEvent(t *testing.T) {
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grepcode"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "src"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
`)

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "grepcode src"},
	})

	if len(violations) == 0 {
		t.Fatalf("expected block when exec returns nonzero")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected exactly one fork per event, got %d", runner.Calls())
	}
}

func TestExecExitCodeMapsToBlockAllow(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	blocking := evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil), rule, payload)
	if len(blocking) == 0 {
		t.Fatalf("exit 1 should block under default block_on")
	}

	allowing := evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 0}, nil), rule, payload)
	if len(allowing) != 0 {
		t.Fatalf("exit 0 should allow under default block_on, got %d", len(allowing))
	}
}

func TestExecBlockOnZeroInverts(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("block_on = \"zero\"\ncache_ttl_ms = 0"))
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	if len(evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 0}, nil), rule, payload)) == 0 {
		t.Fatalf("exit 0 should block under block_on=zero")
	}
	if len(evalRule(newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil), rule, payload)) != 0 {
		t.Fatalf("exit 1 should allow under block_on=zero")
	}
}

func TestExecOnErrorPolicies(t *testing.T) {
	payload := map[string]any{"tool_input": map[string]any{"command": "grepcode here"}}

	open := loadExecRule(t, execRuleTOML("on_error = \"open\"\ncache_ttl_ms = 0"))
	if len(evalRule(newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout), open, payload)) != 0 {
		t.Fatalf("on_error=open should allow on timeout")
	}

	closed := loadExecRule(t, execRuleTOML("on_error = \"closed\"\ncache_ttl_ms = 0"))
	if len(evalRule(newCountingRunner(execconcern.RunResult{}, execconcern.ErrTimeout), closed, payload)) == 0 {
		t.Fatalf("on_error=closed should block on timeout")
	}
}

func TestExecMessageOverride(t *testing.T) {
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 0"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1, Stdout: "codebase X not approved\nmore detail\n"}, nil)

	violations := evalRule(runner, rule, map[string]any{
		"tool_input": map[string]any{"command": "grepcode here"},
	})

	if len(violations) == 0 {
		t.Fatalf("expected block")
	}
	for _, v := range violations {
		if v.Message != "codebase X not approved" {
			t.Fatalf("expected stdout message override, got %q", v.Message)
		}
	}
}

func TestExecCrossEventCacheReuse(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)

	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both events to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected one fork served from cache, got %d", runner.Calls())
	}

	// A fresh runtime (as built on config reload) resets the cache and forks again.
	reloaded := rules.NewExecRuntime(runner, nil)
	reloadCtx := rules.WithExecRuntime(context.Background(), reloaded)
	rules.EvaluateAll(reloadCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if runner.Calls() != 2 {
		t.Fatalf("expected a fresh runtime to fork again, got %d", runner.Calls())
	}
}

func TestExecErrorOutcomeNotCached(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("on_error = \"open\"\ncache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{}, nil)
	runner.responses = []runnerResponse{
		{res: execconcern.RunResult{}, err: execconcern.ErrTimeout},
		{res: execconcern.RunResult{ExitCode: 0}, err: nil},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)

	if runner.Calls() != 2 {
		t.Fatalf("error outcome must not be cached, expected 2 forks, got %d", runner.Calls())
	}
}

func TestExecCanonicalCacheKeySharedAcrossSymlinkAliases(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)

	viaLink := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(map[string]any{
		"effective_cwd": link,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}), []config.Rule{rule}, nil)
	viaReal := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(map[string]any{
		"effective_cwd": real,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}), []config.Rule{rule}, nil)

	if len(viaLink) == 0 || len(viaReal) == 0 {
		t.Fatalf("expected both aliases to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("symlink aliases should share one canonical cache entry, forked %d times", runner.Calls())
	}
}

func TestExecStaleWhileRevalidate(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1"))
	runner := &countingRunner{responses: []runnerResponse{
		{res: execconcern.RunResult{ExitCode: 1}}, // cold: block
		{res: execconcern.RunResult{ExitCode: 0}}, // background refresh: allow
	}}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	cold := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(cold) == 0 {
		t.Fatalf("cold event should block on the synchronous verdict")
	}
	if runner.Calls() != 1 {
		t.Fatalf("cold event should fork once synchronously, got %d", runner.Calls())
	}

	time.Sleep(20 * time.Millisecond) // entry is now stale

	stale := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(stale) == 0 {
		t.Fatalf("stale event should serve the cached block while revalidating")
	}

	deadline := time.Now().Add(2 * time.Second)
	for runner.Calls() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("background refresh never ran, calls=%d", runner.Calls())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // let the refresh's cacheStore settle

	after := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(after) != 0 {
		t.Fatalf("after the background refresh the cached verdict should allow, got %d violations", len(after))
	}
}
