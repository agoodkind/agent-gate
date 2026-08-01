package rules_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// mixedTargetRunner fails for one path and returns a blocking exit for another,
// so one expansion contains both an unclassified and a classified target.
type mixedTargetRunner struct {
	mu      sync.Mutex
	failOn  string
	blockOn string
}

func (r *mixedTargetRunner) Run(
	_ context.Context,
	command []string,
	_ time.Duration,
	_ []byte,
	_ []string,
) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, argument := range command {
		if r.failOn != "" && strings.Contains(argument, r.failOn) {
			return execconcern.RunResult{ExitCode: 0, Stdout: ""}, errors.New("spawn failed")
		}
		if strings.Contains(argument, r.blockOn) {
			return execconcern.RunResult{ExitCode: 1, Stdout: ""}, nil
		}
	}
	return execconcern.RunResult{ExitCode: 0, Stdout: ""}, nil
}

func partialAuditConfig(t *testing.T) *config.Config {
	t.Helper()
	body := `
[[rules]]
name = "search-guard"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
field_paths = ["tool_input.command"]
pattern = '''grep'''

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator", "{{item}}"]
for_each = "cmd_read_targets"
match_mode = "any"
block_on = "nonzero"
cache_ttl_ms = 0
search_tools = ["grep"]
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg
}

func decisionFor(t *testing.T, decisions []rules.RuleDecision, name string) rules.RuleDecision {
	t.Helper()
	for _, decision := range decisions {
		if decision.RuleName == name {
			return decision
		}
	}
	t.Fatalf("no decision recorded for %q", name)
	return rules.RuleDecision{}
}

// TestAuditRecordsAPartiallyProbedExpansion is the end-to-end half of AGATE-13.
// The verdict alone was already right; what was missing is that the audit could
// not tell a fully probed expansion from one that reached a decision before an
// unclassified target mattered, so a partial run recorded as complete coverage.
func TestAuditRecordsAPartiallyProbedExpansion(t *testing.T) {
	cfg := partialAuditConfig(t)
	runner := &mixedTargetRunner{mu: sync.Mutex{}, failOn: "unreadable", blockOn: "indexed"}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)

	directory := t.TempDir()
	for _, name := range []string{"unreadable", "indexed"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	fields := rules.FieldSet{
		ToolName: "Bash",
		ToolInputCommand: "grep -rn pattern " +
			filepath.Join(directory, "unreadable") + " " +
			filepath.Join(directory, "indexed"),
		CWD: directory,
	}

	evaluation := rules.EvaluateAllDetailed(
		ctx, "claude", "PreToolUse", fields, cfg.Rules, nil, nil, "test",
	)

	if len(evaluation.Violations) == 0 {
		t.Fatal("the classifiable target should still block")
	}
	decision := decisionFor(t, evaluation.Trace.Deterministic.CheckedRules, "search-guard")
	if !decision.Matched {
		t.Fatalf("decision = %+v, want matched", decision)
	}
	if !decision.PartialError {
		t.Fatal("the audit decision claims full coverage of a partly probed expansion")
	}

	// The persisted output JSON is what an operator later queries, so the flag
	// has to survive serialization rather than only living in memory.
	var output struct {
		Rules []struct {
			RuleName     string `json:"rule_name"`
			PartialError bool   `json:"partial_error"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(evaluation.Trace.Deterministic.OutputJSON, &output); err != nil {
		t.Fatalf("decode deterministic output: %v", err)
	}
	found := false
	for _, rule := range output.Rules {
		if rule.RuleName != "search-guard" {
			continue
		}
		found = true
		if !rule.PartialError {
			t.Fatal("the serialized record omits the partial-failure flag")
		}
	}
	if !found {
		t.Fatalf("the serialized record has no entry for search-guard: %s",
			evaluation.Trace.Deterministic.OutputJSON)
	}
}

// TestAuditOmitsThePartialFlagWhenEveryTargetWasProbed keeps the signal
// meaningful: a fully probed expansion must not carry it.
func TestAuditOmitsThePartialFlagWhenEveryTargetWasProbed(t *testing.T) {
	cfg := partialAuditConfig(t)
	runner := &mixedTargetRunner{mu: sync.Mutex{}, failOn: "", blockOn: "indexed"}
	runtime := rules.NewExecRuntime(runner, nil)
	ctx := rules.WithExecRuntime(context.Background(), runtime)

	directory := t.TempDir()
	for _, name := range []string{"clean", "indexed"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	fields := rules.FieldSet{
		ToolName: "Bash",
		ToolInputCommand: "grep -rn pattern " +
			filepath.Join(directory, "clean") + " " +
			filepath.Join(directory, "indexed"),
		CWD: directory,
	}

	evaluation := rules.EvaluateAllDetailed(
		ctx, "claude", "PreToolUse", fields, cfg.Rules, nil, nil, "test",
	)
	if len(evaluation.Violations) == 0 {
		t.Fatal("the blocking target should block")
	}
	decision := decisionFor(t, evaluation.Trace.Deterministic.CheckedRules, "search-guard")
	if decision.PartialError {
		t.Fatal("a fully probed expansion was recorded as partial")
	}
}
