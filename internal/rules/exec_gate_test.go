package rules_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
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

// slowRunner blocks for a fixed delay before answering, so a test can hold a
// validator past the rule's synchronous budget without a real process.
type slowRunner struct {
	mu    sync.Mutex
	calls int
	delay time.Duration
	res   execconcern.RunResult
}

func (r *slowRunner) Run(ctx context.Context, _ []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case <-time.After(r.delay):
		return r.res, nil
	case <-ctx.Done():
		return execconcern.RunResult{}, ctx.Err()
	}
}

func (r *slowRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

type commandRunnerFunc func([]string) (execconcern.RunResult, error)

type recordingCommandRunner struct {
	mu       sync.Mutex
	calls    int
	commands [][]string
	run      commandRunnerFunc
}

func (r *recordingCommandRunner) Run(_ context.Context, command []string, _ time.Duration, _ []byte, _ []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	r.calls++
	copied := make([]string, len(command))
	copy(copied, command)
	r.commands = append(r.commands, copied)
	run := r.run
	r.mu.Unlock()
	if run == nil {
		return execconcern.RunResult{}, nil
	}
	return run(copied)
}

func (r *recordingCommandRunner) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (r *recordingCommandRunner) Commands() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][]string, 0, len(r.commands))
	for _, command := range r.commands {
		copied := make([]string, len(command))
		copy(copied, command)
		out = append(out, copied)
	}
	return out
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

func TestExecCacheTTLOneSecondReusesHotMemory(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1000"))
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
		t.Fatalf("expected both one-second TTL evaluations to block")
	}
	if runner.Calls() != 1 {
		t.Fatalf("expected one-second hot cache TTL to reuse the validator result, got %d calls", runner.Calls())
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

func TestExecExpiredCacheRecomputesSynchronously(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1"))
	runner := &countingRunner{responses: []runnerResponse{
		{res: execconcern.RunResult{ExitCode: 1}}, // cold: block
		{res: execconcern.RunResult{ExitCode: 0}}, // expired: recompute and allow
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

	expired := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(expired) != 0 {
		t.Fatalf("expired cache should recompute synchronously and allow, got %d violations", len(expired))
	}
	if runner.Calls() != 2 {
		t.Fatalf("expired event should fork once synchronously, got %d calls", runner.Calls())
	}
}

func TestExecCacheCoalescesConcurrentMisses(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := &slowRunner{
		delay: 100 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 1, Stdout: "not indexed\n"},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	const requestCount = 16
	var waitGroup sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
			if len(violations) == 0 {
				errs <- fmt.Errorf("expected blocking verdict")
			}
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if runner.Calls() != 1 {
		t.Fatalf("concurrent miss should fork once, got %d calls", runner.Calls())
	}
}

func TestExecStableCacheKeySurvivesEquivalentRuntimeWithSharedCache(t *testing.T) {
	dir := t.TempDir()
	rule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	firstRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	firstCtx := rules.WithExecRuntime(context.Background(), firstRuntime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}
	first := rules.EvaluateAll(firstCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) == 0 {
		t.Fatalf("first runtime should block")
	}

	reloadedRule := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	secondRuntime := rules.NewExecRuntimeWithCache(runner, nil, store)
	secondCtx := rules.WithExecRuntime(context.Background(), secondRuntime)
	second := rules.EvaluateAll(secondCtx, "claude", "PreToolUse", testFields(payload), []config.Rule{reloadedRule}, nil)
	if len(second) == 0 {
		t.Fatalf("second runtime should block from shared cache")
	}
	if runner.Calls() != 1 {
		t.Fatalf("equivalent runtime should reuse shared hot cache, got %d calls", runner.Calls())
	}
}

func TestExecStableCacheKeySeparatesDifferentFieldPaths(t *testing.T) {
	dir := t.TempDir()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	rulePathOne := loadExecRule(t, `
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
field_paths = ["tool_input.command"]
command = ["/bin/true"]
cache_ttl_ms = 60000
`)
	rulePathTwo := loadExecRule(t, `
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
field_paths = ["command"]
command = ["/bin/true"]
cache_ttl_ms = 60000
`)
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"command":       "grepcode top-level",
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rulePathOne}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rulePathTwo}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both selector variants to block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("different exec field_paths should not share a cache entry, got %d calls", runner.Calls())
	}
}

func TestExecStableCacheKeySeparatesDifferentCacheTTLs(t *testing.T) {
	dir := t.TempDir()
	runner := newCountingRunner(execconcern.RunResult{ExitCode: 1}, nil)
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()

	ruleTTLMinute := loadExecRule(t, execRuleTOML("cache_ttl_ms = 60000"))
	ruleTTLSecond := loadExecRule(t, execRuleTOML("cache_ttl_ms = 1000"))
	runtime := rules.NewExecRuntimeWithCache(runner, nil, store)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"effective_cwd": dir,
		"tool_input":    map[string]any{"command": "grepcode here"},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleTTLMinute}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{ruleTTLSecond}, nil)
	if len(first) == 0 || len(second) == 0 {
		t.Fatalf("expected both TTL variants to block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("different cache_ttl_ms values should not share a cache entry, got %d calls", runner.Calls())
	}
}

func TestExecForEachCmdReadTargetsExpandsItemAndBlocksOnAnyJSONMatch(t *testing.T) {
	firstTarget := t.TempDir()
	secondTarget := t.TempDir()
	wantFirst, err := filepath.EvalSymlinks(firstTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks firstTarget: %v", err)
	}
	wantSecond, err := filepath.EvalSymlinks(secondTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks secondTarget: %v", err)
	}

	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "cmd_read_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "cmd_read_targets"
cache_ttl_ms = 0
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if len(command) < 2 {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
			}
			if command[1] == wantSecond {
				return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil
			}
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + firstTarget + " " + secondTarget},
	})
	if len(violations) == 0 {
		t.Fatalf("expected any matching target to block")
	}
	commands := runner.Commands()
	if len(commands) != 2 {
		t.Fatalf("expected two expanded validator commands, got %d", len(commands))
	}
	if commands[0][1] != wantFirst {
		t.Fatalf("first expanded item = %q, want %q", commands[0][1], wantFirst)
	}
	if commands[1][1] != wantSecond {
		t.Fatalf("second expanded item = %q, want %q", commands[1][1], wantSecond)
	}
}

func TestExecForEachExecTargetsFallsBackToFilePath(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	wantTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 0
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
		},
	}

	evalRule(runner, rule, map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	})

	commands := runner.Commands()
	if len(commands) != 1 {
		t.Fatalf("expected one expanded validator command, got %d", len(commands))
	}
	if commands[0][1] != wantTarget {
		t.Fatalf("expanded exec_targets item = %q, want %q", commands[0][1], wantTarget)
	}
}

func TestExecJSONMatchErrorOutcomeNotCached(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
on_error = "open"
`)
	runner := newCountingRunner(execconcern.RunResult{}, nil)
	runner.responses = []runnerResponse{
		{res: execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":`}, err: nil},
		{res: execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, err: nil},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("invalid JSON predicate output should fail open")
	}
	if len(second) == 0 {
		t.Fatalf("successful follow-up JSON match should block")
	}
	if runner.Calls() != 2 {
		t.Fatalf("errored JSON predicate outcome must not be cached, got %d calls", runner.Calls())
	}
}

func TestExecJSONMatchConcurrentMissesSingleflight(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
`)
	runner := &slowRunner{
		delay: 100 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	const requestCount = 12
	var waitGroup sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
			if len(violations) == 0 {
				errs <- fmt.Errorf("expected blocking verdict")
			}
		}()
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if runner.Calls() != 1 {
		t.Fatalf("concurrent JSON predicate miss should fork once, got %d calls", runner.Calls())
	}
}

func TestExecJSONMatchTimeoutFailsOpen(t *testing.T) {
	target := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_name"]
pattern = "^Grep$"

[[rules.conditions]]
kind = "exec"
command = ["/bin/check-target", "{{item}}"]
for_each = "exec_targets"
match_mode = "any"
stdout_json_field = "searchable"
stdout_json_equals = true
cache_key = "exec_targets"
cache_ttl_ms = 60000
timeout_ms = 50
on_error = "open"
`)
	runner := &slowRunner{
		delay: 150 * time.Millisecond,
		res:   execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`},
	}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":       t.TempDir(),
		"tool_name": "Grep",
		"tool_input": map[string]any{
			"path": target,
		},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("timed-out JSON predicate event should fail open")
	}
}

// A validator that outlives the synchronous budget fails the current event
// open, finishes in the background, and caches its verdict so the next event
// for the same target blocks.
func TestExecGateSlowValidatorFinishesInBackgroundAndCachesBlock(t *testing.T) {
	runner := &slowRunner{delay: 150 * time.Millisecond, res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
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
kind = "exec"
command = ["/bin/true"]
timeout_ms = 50
cache_ttl_ms = 60000
`)

	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grepcode -rn x ."},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("event whose validator exceeds the budget should fail open, got %d violations", len(first))
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		violations := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
		if len(violations) > 0 {
			break // background verdict landed in the cache and now blocks
		}
		if time.Now().After(deadline) {
			t.Fatalf("background verdict never reached the cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExecGateTimeoutKeepsSingleflightUntilBackgroundCompletes(t *testing.T) {
	runner := &slowRunner{delay: 150 * time.Millisecond, res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
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
kind = "exec"
command = ["/bin/true"]
timeout_ms = 50
cache_ttl_ms = 60000
`)

	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)
	payload := map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grepcode -rn x ."},
	}

	first := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if len(first) != 0 {
		t.Fatalf("timed-out first event should fail open, got %d violations", len(first))
	}
	second := rules.EvaluateAll(ctx, "claude", "PreToolUse", testFields(payload), []config.Rule{rule}, nil)
	if runner.Calls() != 1 {
		t.Fatalf("background window should still coalesce to one validator run, got %d calls", runner.Calls())
	}
	if len(second) == 0 {
		t.Fatalf("concurrent follow-up event should reuse the in-flight validator result once it lands")
	}
}

// capturingRunner records the env passed to the validator so a test can assert
// the read targets the gate computed.
type capturingRunner struct {
	mu  sync.Mutex
	env []string
	res execconcern.RunResult
}

func (r *capturingRunner) Run(_ context.Context, _ []string, _ time.Duration, _ []byte, env []string) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.env = env
	return r.res, nil
}

func (r *capturingRunner) readTargets() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kv := range r.env {
		if after, ok := strings.CutPrefix(kv, "AGENT_GATE_READ_TARGETS="); ok {
			return after
		}
	}
	return ""
}

func (r *capturingRunner) envValue(key string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kv := range r.env {
		if after, ok := strings.CutPrefix(kv, key+"="); ok {
			return after, true
		}
	}
	return "", false
}

// TestExecReadTargetsResolveAgainstEffectiveCwd guards the cd-chain fix: a search
// run after `cd /other` must be attributed to /other, not the session cwd, so the
// validator checks the directory the search actually reads.
func TestExecReadTargetsResolveAgainstEffectiveCwd(t *testing.T) {
	sessionDir := t.TempDir()
	otherDir := t.TempDir()
	wantTarget, err := filepath.EvalSymlinks(otherDir)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}

	runner := &capturingRunner{res: execconcern.RunResult{ExitCode: 0}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
search_tools = ["grep"]
`)

	evalRule(runner, rule, map[string]any{
		"cwd":        sessionDir,
		"tool_input": map[string]any{"command": "cd " + otherDir + " && grep -rn x ."},
	})

	got := runner.readTargets()
	if got != wantTarget {
		t.Fatalf("read targets = %q, want the cd dir %q (not session %q)", got, wantTarget, sessionDir)
	}
}

// A grep behind an unresolvable cd must still reach the validator (the spawn
// must not die on the marker's NUL byte) and the validator must see the
// unknown directory as an empty AGENT_GATE_CWD per the validator contract.
func TestExecGateUnresolvableCwdRunsValidatorWithEmptyCwd(t *testing.T) {
	target := t.TempDir()
	runner := &capturingRunner{res: execconcern.RunResult{ExitCode: 1, Stdout: "indexed\n"}}
	rule := loadExecRule(t, `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "static message"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
cache_ttl_ms = 0
`)

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": `cd "$(echo /tmp)" && grep -rn x ` + target},
	})

	if len(violations) == 0 {
		t.Fatalf("validator exit 1 behind an unresolvable cd should block; the spawn must not fail open")
	}
	cwd, found := runner.envValue("AGENT_GATE_CWD")
	if !found {
		t.Fatalf("validator env missing AGENT_GATE_CWD")
	}
	if cwd != "" {
		t.Fatalf("AGENT_GATE_CWD = %q, want empty for an unresolvable cwd", cwd)
	}
}
