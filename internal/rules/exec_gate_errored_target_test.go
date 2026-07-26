package rules_test

import (
	"os"
	"path/filepath"
	"testing"

	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// TestExecForEachAnyErroredTargetDoesNotVetoOthers covers the case where one
// expanded target errors while another is a genuine match. Under
// match_mode = "any" the errored target must not decide the expansion: with
// on_error = "open" an early return would allow the command without ever
// probing the target that blocks, which is how a code search whose command text
// also yields an unresolvable target reached the tool unblocked.
func TestExecForEachAnyErroredTargetDoesNotVetoOthers(t *testing.T) {
	erroringTarget := filepath.Join(t.TempDir(), "repo-unknown")
	matchingTarget := filepath.Join(t.TempDir(), "repo-indexed")
	for _, dir := range []string{erroringTarget, matchingTarget} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}
	wantErroring, err := filepath.EvalSymlinks(erroringTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks erroringTarget: %v", err)
	}
	wantMatching, err := filepath.EvalSymlinks(matchingTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks matchingTarget: %v", err)
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
on_error = "open"
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if command[1] == wantErroring {
				return execconcern.RunResult{ExitCode: 0, Stdout: "not json"}, nil
			}
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":true}`}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + erroringTarget + " " + matchingTarget},
	})
	if len(violations) == 0 {
		t.Fatal("an errored target vetoed a matching target under match_mode=any")
	}
	// The contract is that the matching target is reached, not that a fixed
	// number of validators ran. Asserting a count would pin the order the two
	// targets happen to resolve in, and would also forbid a later change that
	// probes targets concurrently or stops early once one blocks.
	if !probedTarget(runner, wantMatching) {
		t.Fatalf("matching target %q was never probed, commands = %v", wantMatching, runner.Commands())
	}
}

// probedTarget reports whether the runner was asked to validate a given target.
func probedTarget(runner *recordingCommandRunner, target string) bool {
	for _, command := range runner.Commands() {
		for _, argument := range command {
			if argument == target {
				return true
			}
		}
	}
	return false
}

// TestExecForEachAnyFailClosedStopsAtFirstErroredTarget covers the bound on
// validator work: under on_error = "closed" an errored target already decides
// the condition, so the remaining targets are not probed. Continuing would cost
// one validator run per remaining target, each up to the background timeout
// times retry_count, under a context that cannot be cancelled and while the
// singleflight entry for this cache key is held.
func TestExecForEachAnyFailClosedStopsAtFirstErroredTarget(t *testing.T) {
	erroringTarget := filepath.Join(t.TempDir(), "repo-unknown")
	laterTarget := filepath.Join(t.TempDir(), "repo-indexed")
	for _, dir := range []string{erroringTarget, laterTarget} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
	}
	wantErroring, err := filepath.EvalSymlinks(erroringTarget)
	if err != nil {
		t.Fatalf("EvalSymlinks erroringTarget: %v", err)
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
on_error = "closed"
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(command []string) (execconcern.RunResult, error) {
			if command[1] == wantErroring {
				return execconcern.RunResult{ExitCode: 0, Stdout: "not json"}, nil
			}
			return execconcern.RunResult{ExitCode: 0, Stdout: `{"searchable":false}`}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + erroringTarget + " " + laterTarget},
	})
	if len(violations) == 0 {
		t.Fatal("a fail-closed rule should block once a target errors")
	}
	if len(runner.Commands()) != 1 {
		t.Fatalf("fail-closed should stop at the errored target, got %d commands", len(runner.Commands()))
	}
}

// TestExecForEachAnyAllTargetsErroredStaysErrored covers the fail-open contract:
// when no target matches and every target errored, the condition still reports
// the errored verdict so on_error decides, rather than reporting a clean allow.
func TestExecForEachAnyAllTargetsErroredStaysErrored(t *testing.T) {
	targetOne := filepath.Join(t.TempDir(), "repo-one")
	targetTwo := filepath.Join(t.TempDir(), "repo-two")
	for _, dir := range []string{targetOne, targetTwo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll %s: %v", dir, err)
		}
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
on_error = "closed"
search_tools = ["grep"]
`)
	runner := &recordingCommandRunner{
		run: func(_ []string) (execconcern.RunResult, error) {
			return execconcern.RunResult{ExitCode: 0, Stdout: "not json"}, nil
		},
	}

	violations := evalRule(runner, rule, map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "grep -rn x " + targetOne + " " + targetTwo},
	})
	if len(violations) == 0 {
		t.Fatal("all targets errored under on_error=closed should block")
	}
	if len(runner.Commands()) == 0 {
		t.Fatal("no target was probed at all")
	}
}
