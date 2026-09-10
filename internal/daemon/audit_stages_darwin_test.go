package daemon

import (
	"context"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
)

func TestNormalDeferredReceiptCommitsFourStages(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := newReadyTestServer(newDiscardLogger(), auditOnlyDaemonTestConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	<-server.auditStarted
	store := server.runtime.Load().intakeStore.(*sqliteIntakeStore)
	counter := qualificationCommitCounter(t, store.Handle())
	response, err := server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{ProviderHint: "codex", RawJson: []byte(`{"session_id":"four-stages","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`)})
	if err != nil || response.GetExitCode() != 0 {
		t.Fatalf("hook response=%v, error=%v", response, err)
	}
	waitForNoPendingIntake(t, server)
	server.Close()
	if count := counter.Load(); count != 4 {
		t.Fatalf("normal receipt commits=%d, want append, hot, claim, deferred (4)", count)
	}
	t.Logf("real SQLite commit callbacks=%d", counter.Load())
}
