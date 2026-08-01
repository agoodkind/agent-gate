package hook_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

// TestFailOpenIsVisibleToTheAgent is the regression for the ten hour outage.
// The daemon was down, every call was allowed, and the hook exited 0 with an
// empty body, which is byte for byte what a call that passed every rule looks
// like. An allow that nobody evaluated must not be silent.
func TestFailOpenIsVisibleToTheAgent(t *testing.T) {
	evaluated := hook.RenderResponse(hook.ResponseRequest{
		System: hook.SystemClaude, EventName: "PreToolUse",
		Decision: hook.ResponseDecisionAllow,
	})
	unevaluated := hook.FailOpenResponse(
		hook.SystemClaude, "PreToolUse",
		"agent-gate: daemon unavailable: no socket",
		hook.FailOpenReasonDaemonUnavailable,
	)

	if unevaluated.ExitCode != 0 {
		t.Fatalf("fail-open exit code = %d, want 0; it must not block the call",
			unevaluated.ExitCode)
	}
	if string(unevaluated.Stdout) == string(evaluated.Stdout) {
		t.Fatalf("an unevaluated allow is byte identical to an evaluated one: %q",
			unevaluated.Stdout)
	}

	var payload struct {
		SystemMessage string `json:"systemMessage"`
	}
	if err := json.Unmarshal(unevaluated.Stdout, &payload); err != nil {
		t.Fatalf("fail-open stdout is not JSON: %v (%q)", err, unevaluated.Stdout)
	}
	if payload.SystemMessage == "" {
		t.Fatal("fail-open carries no systemMessage, so the agent sees nothing")
	}
	if !strings.Contains(payload.SystemMessage, "no rule was enforced") {
		t.Fatalf("systemMessage = %q, want it to say no rule was enforced",
			payload.SystemMessage)
	}
	if !strings.Contains(payload.SystemMessage, string(hook.FailOpenReasonDaemonUnavailable)) {
		t.Fatalf("systemMessage = %q, want it to name the reason", payload.SystemMessage)
	}
}

// TestBlockIsUnaffectedByTheNotice keeps the change scoped: a real block still
// exits 2 with its diagnostic on stderr.
func TestBlockIsUnaffectedByTheNotice(t *testing.T) {
	blocked := hook.RenderResponse(hook.ResponseRequest{
		System: hook.SystemClaude, EventName: "PreToolUse",
		Decision: hook.ResponseDecisionBlock, DiagnosticText: "nope",
	})
	if blocked.ExitCode != 2 {
		t.Fatalf("block exit code = %d, want 2", blocked.ExitCode)
	}
	if !strings.Contains(string(blocked.Stderr), "nope") {
		t.Fatalf("block stderr = %q, want the diagnostic", blocked.Stderr)
	}
}

// TestFailOpenLeavesADurableRecord is the other half of the outage: the daemon
// that writes the audit database is the daemon that is down, so nothing was
// recorded and a later query returned a clean empty result. That reads as no
// violations when it means no checks. The record must be written by the hook.
func TestFailOpenLeavesADurableRecord(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	summary, err := hook.FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary before any record: %v", err)
	}
	if summary.Count != 0 || summary.Earliest != "" || summary.Latest != "" {
		t.Fatalf("clean state reported %d records (%s..%s)",
			summary.Count, summary.Earliest, summary.Latest)
	}
	if summary.Truncated {
		t.Fatal("clean state reported a truncated history")
	}

	for range 3 {
		hook.RecordFailOpen(
			hook.FailOpenReasonDaemonUnavailable, hook.SystemClaude,
			"PreToolUse", "Bash", "/repo", "no socket",
		)
	}

	summary, err = hook.FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary after recording: %v", err)
	}
	if summary.Count != 3 {
		t.Fatalf("records = %d, want 3", summary.Count)
	}
	if summary.Earliest == "" || summary.Latest == "" {
		t.Fatalf("record window = %q..%q, want both ends set",
			summary.Earliest, summary.Latest)
	}
	if summary.Truncated {
		t.Fatal("three small records should not have hit the size cap")
	}

	path := filepath.Join(stateHome, "agent-gate", "fail-open.jsonl")
	t.Logf("records written to %s", path)
}

// TestRecordFailOpenIgnoresAnEmptyReason keeps the file free of entries for
// calls that were actually evaluated.
func TestRecordFailOpenIgnoresAnEmptyReason(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	hook.RecordFailOpen("", hook.SystemClaude, "PreToolUse", "Bash", "/repo", "")
	summary, err := hook.FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Count != 0 {
		t.Fatalf("records = %d, want 0 for an evaluated call", summary.Count)
	}
}
