package rules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// perTargetRunner errors for one target and returns a clean blocking exit for
// another, which is the mixed expansion AGATE-13 describes.
type perTargetRunner struct {
	mu      sync.Mutex
	failOn  string
	blockOn string
}

func (r *perTargetRunner) Run(
	_ context.Context,
	command []string,
	_ time.Duration,
	_ []byte,
	_ []string,
) (execconcern.RunResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, argument := range command {
		if r.failOn != "" && argument == r.failOn {
			return execconcern.RunResult{ExitCode: 0, Stdout: ""}, errors.New("spawn failed")
		}
		if argument == r.blockOn {
			return execconcern.RunResult{ExitCode: 1, Stdout: ""}, nil
		}
	}
	return execconcern.RunResult{ExitCode: 0, Stdout: ""}, nil
}

// forEachCondition loads a real for_each exec condition, so the compiled
// selector and block_on are wired exactly as a config would produce them.
func forEachCondition(t *testing.T) *config.Condition {
	t.Helper()
	body := `
[[rules]]
name = "partial"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''anything'''

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
	return &cfg.Rules[0].Conditions[0]
}

// TestBlockingVerdictReportsAnUnclassifiedSibling is the regression for the
// audit-fidelity half of AGATE-13. Under match_mode = "any" a blocking target
// returns immediately, and the verdict used to carry no trace that an earlier
// target could not be classified, so the evaluation recorded as fully probed.
//
// The verdict itself must not change: letting one unresolvable target veto the
// rest is the veto bug AGATE-6 fixed.
func TestBlockingVerdictReportsAnUnclassifiedSibling(t *testing.T) {
	runner := &perTargetRunner{mu: sync.Mutex{}, failOn: "/repo/unreadable", blockOn: "/repo/indexed"}
	runtime := NewExecRuntime(runner, nil)

	verdict := runtime.runExpandedCommands(
		context.Background(), "partial", forEachCondition(t),
		[][]string{
			{"/bin/validator", "/repo/unreadable"},
			{"/bin/validator", "/repo/indexed"},
		},
		nil, nil,
	)

	if !verdict.Block {
		t.Fatal("the classifiable target should still block")
	}
	if verdict.Errored {
		t.Fatal("Errored must stay false; one unclassified target does not veto the rest")
	}
	if !verdict.PartialError {
		t.Fatal("PartialError = false, so the record claims the whole expansion was probed")
	}
}

// TestFullyProbedExpansionReportsNoPartialError keeps the signal meaningful: an
// expansion where every target was classified must not claim otherwise.
func TestFullyProbedExpansionReportsNoPartialError(t *testing.T) {
	runner := &perTargetRunner{mu: sync.Mutex{}, failOn: "", blockOn: "/repo/indexed"}
	runtime := NewExecRuntime(runner, nil)

	verdict := runtime.runExpandedCommands(
		context.Background(), "partial", forEachCondition(t),
		[][]string{
			{"/bin/validator", "/repo/clean"},
			{"/bin/validator", "/repo/indexed"},
		},
		nil, nil,
	)

	if !verdict.Block {
		t.Fatal("the blocking target should block")
	}
	if verdict.PartialError {
		t.Fatal("PartialError = true for an expansion where every target was classified")
	}
}
