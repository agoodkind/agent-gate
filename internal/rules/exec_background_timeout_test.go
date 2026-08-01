package rules

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

// timeoutRecordingRunner records the deadline every validator fork was given,
// so these tests assert on the timeout actually applied rather than on a call
// count. runErr drives the retry loop, because a clean non-zero exit is a
// verdict while a non-nil error is the errored outcome that retries.
type timeoutRecordingRunner struct {
	mu       sync.Mutex
	timeouts []time.Duration
	runErr   error
}

func (r *timeoutRecordingRunner) Run(
	_ context.Context,
	_ []string,
	timeout time.Duration,
	_ []byte,
	_ []string,
) (execconcern.RunResult, error) {
	r.mu.Lock()
	r.timeouts = append(r.timeouts, timeout)
	r.mu.Unlock()
	return execconcern.RunResult{ExitCode: 0, Stdout: ""}, r.runErr
}

func (r *timeoutRecordingRunner) seen() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Duration(nil), r.timeouts...)
}

func newTimeoutRunner(runErr error) *timeoutRecordingRunner {
	return &timeoutRecordingRunner{mu: sync.Mutex{}, timeouts: nil, runErr: runErr}
}

// TestBackgroundTimeoutDefaultsWithoutTheSetter covers a runtime built outside
// the daemon, which never has the setter called. An unset value must fall back
// to the documented default, not to zero, because a zero deadline would kill
// every detached validator immediately.
func TestBackgroundTimeoutDefaultsWithoutTheSetter(t *testing.T) {
	runner := newTimeoutRunner(nil)
	runtime := NewExecRuntime(runner, nil)
	runtime.runExpandedCommandWithRetry(
		context.Background(), "rule", &config.Condition{}, []string{"/bin/true"}, nil, nil,
	)

	seen := runner.seen()
	if len(seen) == 0 {
		t.Fatal("no validator run recorded")
	}
	want := config.DefaultExecBackgroundMs * time.Millisecond
	if seen[0] != want {
		t.Fatalf("background timeout = %s, want the documented default %s", seen[0], want)
	}
}

// TestBackgroundTimeoutUsesTheConfiguredValue covers the setter the daemon
// calls when it builds a runtime snapshot.
func TestBackgroundTimeoutUsesTheConfiguredValue(t *testing.T) {
	runner := newTimeoutRunner(nil)
	runtime := NewExecRuntime(runner, nil)
	runtime.SetBackgroundTimeout(7 * time.Second)
	runtime.runExpandedCommandWithRetry(
		context.Background(), "rule", &config.Condition{}, []string{"/bin/true"}, nil, nil,
	)

	seen := runner.seen()
	if len(seen) == 0 {
		t.Fatal("no validator run recorded")
	}
	if seen[0] != 7*time.Second {
		t.Fatalf("background timeout = %s, want 7s", seen[0])
	}
}

// TestBackgroundTimeoutIsStableAcrossRetries is the regression for the review
// finding: the deadline used to be produced by loading and recompiling the
// whole config, inside the retry loop, so a condition with retries paid that
// cost once per attempt and could see a different value mid-run if the file
// changed. Every attempt must now read the same runtime-held value.
func TestBackgroundTimeoutIsStableAcrossRetries(t *testing.T) {
	runner := newTimeoutRunner(errors.New("spawn failed"))
	runtime := NewExecRuntime(runner, nil)
	runtime.SetBackgroundTimeout(5 * time.Second)
	condition := &config.Condition{RetryCount: 2}
	runtime.runExpandedCommandWithRetry(
		context.Background(), "rule", condition, []string{"/bin/false"}, nil, nil,
	)

	seen := runner.seen()
	if len(seen) != 3 {
		t.Fatalf("attempts = %d, want 3 (retry_count 2 plus the first try)", len(seen))
	}
	for attempt, timeout := range seen {
		if timeout != 5*time.Second {
			t.Fatalf("attempt %d timeout = %s, want 5s on every attempt", attempt, timeout)
		}
	}
}
