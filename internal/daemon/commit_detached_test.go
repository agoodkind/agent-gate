package daemon

import (
	"context"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

// TestVerdictSurvivesACallerThatLeft is the regression for a computed verdict
// being erased by the caller's deadline.
//
// Under load an evaluation can exceed the validator budget, and the caller's
// deadline expires shortly after. The verdict already exists at that point, but
// the commit ran on the request context, so it could not begin a transaction
// and the record was dropped. The audit then showed nothing for a call a rule
// had matched.
//
// Measured on the live daemon on 2026-08-05: 92 budget overruns since
// 2026-07-30, 51 on the search rule, each followed by a discarded verdict.
func TestVerdictSurvivesACallerThatLeft(t *testing.T) {
	setDaemonTestDirs(t)
	writeConfig(t, config.Path(), `
[audit]
enabled = false

[[rules]]
name = "block-alpha"
claude_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "alpha blocked"
`)
	cfg, err := config.LoadDegraded()
	if err != nil {
		t.Fatalf("LoadDegraded: %v", err)
	}
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	server.runtime.Load().evaluationRecorder = recorder

	// The caller is already gone by the time the commit runs, which is the
	// state a deadline expiry leaves behind.
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	response, err := server.EvaluateHook(cancelled, &daemonpb.EvaluateHookRequest{
		RawJson: []byte(`{"session_id":"s1","hook_event_name":"PreToolUse",` +
			`"tool_name":"Bash","tool_input":{"command":"alpha"}}`),
		ProviderHint: hook.SystemClaude.String(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	// The record has to exist, because that is the whole point: a verdict the
	// rules computed must not vanish because nobody was left to receive it.
	deadline := time.Now().Add(3 * time.Second)
	for len(recorder.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	records := recorder.snapshot()
	if len(records) == 0 {
		t.Fatalf("the verdict was discarded because the caller left; response = %+v",
			response)
	}
}
