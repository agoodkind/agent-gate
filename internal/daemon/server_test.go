package daemon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/regex"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func boolPtr(v bool) *bool { return &v }

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func setDaemonTestDirs(t testing.TB) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
}

func daemonTestConfig(t testing.TB) *config.Config {
	t.Helper()
	re, err := regex.Compile(`go test \./\.\.\.`)
	if err != nil {
		t.Fatalf("compile regex: %v", err)
	}
	return &config.Config{
		Audit: config.Audit{Enabled: boolPtr(false)},
		Rules: []config.Rule{
			config.NewSimpleRule(
				"no-broad-go-test",
				`go test \./\.\.\.`,
				re,
				nil,
				[]string{"tool_input.command"},
				"block",
				"Use make test for full project runs.",
			),
		},
	}
}

func TestRuntimeSnapshotsShareInferenceRuntime(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	first := server.runtime.Load()
	if first == nil || first.inferRuntime != server.inferRuntime {
		t.Fatal("initial snapshot does not use the server inference runtime")
	}
	second, err := newRuntimeSnapshot(context.Background(), cfg, newDiscardLogger(), server.hotKV, server.inferRuntime)
	if err != nil {
		t.Fatalf("newRuntimeSnapshot: %v", err)
	}
	defer second.close(context.Background(), newDiscardLogger())
	if second.inferRuntime != first.inferRuntime {
		t.Fatal("replacement snapshot did not preserve inference channels")
	}
}

func emdashDaemonTestConfig(t testing.TB) *config.Config {
	t.Helper()
	pattern := `[\x{2010}-\x{2015}]`
	re, err := regex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile emdash regex: %v", err)
	}
	return &config.Config{
		Audit: config.Audit{Enabled: boolPtr(false)},
		Rules: []config.Rule{
			config.NewSimpleRule(
				"no-emdashes",
				pattern,
				re,
				nil,
				[]string{"tool_input.new_string", "edits[*].new_string", "last_assistant_message"},
				"block",
				"No typographic dashes.",
			),
		},
	}
}

func codexStopAuditDaemonTestConfig(t testing.TB) *config.Config {
	t.Helper()
	re, err := regex.Compile(`this--is`)
	if err != nil {
		t.Fatalf("compile regex: %v", err)
	}
	return &config.Config{
		Audit: config.Audit{Enabled: boolPtr(true)},
		Rules: []config.Rule{
			config.NewSimpleRule(
				"stop-double-hyphen",
				`this--is`,
				re,
				[]string{"Stop"},
				[]string{"last_assistant_message"},
				"block",
				"Rewrite the stop text.",
			),
		},
	}
}

// An unresolvable cd makes the effective-cwd field the shelldecomp marker,
// which begins with a NUL byte. The intake record must store the unknown
// directory as an empty string, not leak the marker into SQLite.
func TestBuildIntakeRecordMapsUnresolvableCwdToEmpty(t *testing.T) {
	raw := []byte(`{
		"hook_event_name": "PreToolUse",
		"session_id": "test-session",
		"cwd": "/tmp",
		"tool_name": "Bash",
		"tool_input": {"command": "cd \"$(echo /tmp)\" && grep -rn x ."}
	}`)

	record, err := buildIntakeRecord(raw, "claude", map[string]string{})
	if err != nil {
		t.Fatalf("buildIntakeRecord: %v", err)
	}
	if record.Operation.EffectiveCWD != "" {
		t.Fatalf("EffectiveCWD = %q, want empty for an unresolvable cwd", record.Operation.EffectiveCWD)
	}
}

func TestEvaluateHook_DaemonOwnsEnforcement(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0 for Codex JSON-deny response", resp.ExitCode)
	}
	if got := string(resp.StdoutData); !strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "no-broad-go-test") {
		t.Fatalf("stdout missing Codex deny response: %s", got)
	}
}

func TestEvaluateHook_InvalidJSONFailsClosed(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{RawJson: []byte(`{`)})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 2 {
		t.Fatalf("exit_code = %d, want 2", resp.ExitCode)
	}
	if !strings.Contains(string(resp.StderrData), "parse stdin JSON") {
		t.Fatalf("stderr missing parse error: %q", string(resp.StderrData))
	}
}

func TestEvaluateHook_OverloadFailsOpen(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	snapshot := setEvaluateAdmissionForTest(t, srv, 1, time.Millisecond)
	snapshot.evaluateSlots <- struct{}{}

	start := time.Now()
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("EvaluateHook overload took %s, want bounded wait", elapsed)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want fail-open exit 0", resp.ExitCode)
	}
	if len(resp.StdoutData) != 0 || len(resp.StderrData) != 0 {
		t.Fatalf("overload response wrote stdout=%q stderr=%q, want transport-neutral allow", string(resp.StdoutData), string(resp.StderrData))
	}
}

func TestEvaluateHook_ConcurrentBurstCompletes(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	setEvaluateAdmissionForTest(t, srv, 4, 50*time.Millisecond)

	const requestCount = 64
	cwd := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, requestCount)
	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
				RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
				ProviderHint: "codex",
				Cwd:          cwd,
				EnvFingerprint: map[string]string{
					"CODEX_THREAD_ID": "test-thread",
				},
			})
			if err != nil {
				errs <- err
				return
			}
			if resp.ExitCode != 0 {
				errs <- fmt.Errorf("unexpected non-zero exit: %d", resp.ExitCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent EvaluateHook: %v", err)
		}
	}
}

func TestKVHotStoreRPCs(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	cfg.Performance.Hook.Cache.MaxEntries = 16
	cfg.Performance.Hook.Cache.MaxValueBytes = 64
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	setResp, err := srv.KVSet(context.Background(), &daemonpb.KVSetRequest{
		Namespace: "test",
		Key:       "repo",
		Value:     []byte("indexed"),
		Mode:      "NX",
		TtlMs:     1000,
	})
	if err != nil {
		t.Fatalf("KVSet: %v", err)
	}
	if !setResp.GetStored() {
		t.Fatal("KVSet stored = false, want true")
	}

	skipped, err := srv.KVSet(context.Background(), &daemonpb.KVSetRequest{
		Namespace: "test",
		Key:       "repo",
		Value:     []byte("other"),
		Mode:      "NX",
	})
	if err != nil {
		t.Fatalf("KVSet NX existing: %v", err)
	}
	if skipped.GetStored() {
		t.Fatal("KVSet NX existing stored = true, want false")
	}

	getResp, err := srv.KVGet(context.Background(), &daemonpb.KVGetRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVGet: %v", err)
	}
	if !getResp.GetFound() || string(getResp.GetEntry().GetValue()) != "indexed" {
		t.Fatalf("KVGet found=%v value=%q, want indexed", getResp.GetFound(), string(getResp.GetEntry().GetValue()))
	}
	if getResp.GetEntry().GetPttlMs() <= 0 {
		t.Fatalf("PTTL = %d, want positive", getResp.GetEntry().GetPttlMs())
	}

	ttlResp, err := srv.KVTTL(context.Background(), &daemonpb.KVGetRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVTTL: %v", err)
	}
	if ttlResp.GetTtl() < 0 {
		t.Fatalf("KVTTL ttl = %d, want non-negative active TTL", ttlResp.GetTtl())
	}

	pttlResp, err := srv.KVPTTL(context.Background(), &daemonpb.KVGetRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVPTTL: %v", err)
	}
	if pttlResp.GetPttl() <= 0 {
		t.Fatalf("KVPTTL pttl = %d, want positive", pttlResp.GetPttl())
	}

	exists, err := srv.KVExists(context.Background(), &daemonpb.KVExistsRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVExists: %v", err)
	}
	if !exists.GetExists() {
		t.Fatal("KVExists = false, want true")
	}

	deleted, err := srv.KVDelete(context.Background(), &daemonpb.KVDeleteRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVDelete: %v", err)
	}
	if !deleted.GetDeleted() {
		t.Fatal("KVDelete = false, want true")
	}

	missingTTL, err := srv.KVTTL(context.Background(), &daemonpb.KVGetRequest{Namespace: "test", Key: "repo"})
	if err != nil {
		t.Fatalf("KVTTL missing: %v", err)
	}
	if missingTTL.GetTtl() != -2 {
		t.Fatalf("KVTTL missing = %d, want -2", missingTTL.GetTtl())
	}
}

func TestKVSetRejectsInvalidMode(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	_, err = srv.KVSet(context.Background(), &daemonpb.KVSetRequest{
		Namespace: "test",
		Key:       "repo",
		Value:     []byte("indexed"),
		Mode:      "BAD",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("KVSet invalid mode status = %v, want %v", status.Code(err), codes.InvalidArgument)
	}
}

func TestKVListRejectsNegativeLimit(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	_, err = srv.KVList(context.Background(), &daemonpb.KVListRequest{
		Namespace: "test",
		Limit:     -1,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("KVList negative limit status = %v, want %v", status.Code(err), codes.InvalidArgument)
	}
}

func TestEvaluateHook_DeferredWorkerCompletesFreshEvent(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	cfg.Performance.Hook.DeferredWorkers = 1
	cfg.Performance.Hook.DeferredQueueLimit = 4
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "test-thread",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", resp.ExitCode)
	}

	waitForNoPendingIntake(t, srv)
}

func TestHotPathBlocksBeforeDeferredQueue(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	hotCalled := false
	setHotEvaluatorForTest(t, srv, func(ctx context.Context, rawJSON []byte, cfg *config.Config, hint hook.System, getenv func(string) string, eventID string) hook.HotEvaluation {
		hotCalled = true
		return hook.EvaluateHotWithEventID(ctx, rawJSON, cfg, hint, getenv, eventID)
	})
	replaceIntakeStoreForTest(t, srv, failingIntakeStore{})

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "test-thread",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if hotCalled {
		t.Fatal("hot evaluator ran after intake append failed, want append-before-eval fail-open")
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want fail-open exit 0", resp.ExitCode)
	}
	if len(resp.StdoutData) != 0 || len(resp.StderrData) != 0 {
		t.Fatalf("append failure response wrote stdout=%q stderr=%q, want transport-neutral allow", string(resp.StdoutData), string(resp.StderrData))
	}
}

func TestDeferredReplayAfterRestart(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := auditOnlyDaemonTestConfig(t)
	cfg.Performance.Hook.DeferredWorkers = 0
	cfg.Performance.Hook.DeferredQueueLimit = 4

	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "test-thread",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0", resp.ExitCode)
	}
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	pendingBeforeClose, err := snapshot.intakeStore.ListPending(context.Background())
	if err != nil {
		t.Fatalf("ListPending before close: %v", err)
	}
	if len(pendingBeforeClose) != 1 {
		t.Fatalf("pending before close = %d, want 1", len(pendingBeforeClose))
	}
	srv.Close()

	cfg.Performance.Hook.DeferredWorkers = 1
	srv, err = New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New restart: %v", err)
	}
	defer srv.Close()

	waitForAuditMessages(t, cfg, "hook.audit_violation", "hook.allowed")
	pendingAfterReplay, err := srv.runtime.Load().intakeStore.ListPending(context.Background())
	if err != nil {
		t.Fatalf("ListPending after replay: %v", err)
	}
	if len(pendingAfterReplay) != 0 {
		t.Fatalf("pending after replay = %d, want 0", len(pendingAfterReplay))
	}
}

func TestSyncAndDeferredRulesStaySeparated(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := mixedSyncDeferredDaemonTestConfig(t)
	cfg.Performance.Hook.DeferredWorkers = 1
	cfg.Performance.Hook.DeferredQueueLimit = 4

	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "test-thread",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if got := string(resp.StdoutData); !strings.Contains(got, `"permissionDecision":"deny"`) || !strings.Contains(got, "no-broad-go-test") {
		t.Fatalf("stdout missing Codex deny response: %s", got)
	}

	waitForAuditMessages(t, cfg, "hook.blocked")
	events, _, err := audit.Query(cfg, audit.QueryFilter{Limit: 20})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	for _, event := range events {
		if event.Message == "hook.audit_violation" {
			t.Fatalf("unexpected audit-only event alongside sync block: %+v", event)
		}
	}
}

func TestPolicyBlockDoesNotFailOpenWhenHotSlotsAvailable(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	setEvaluateAdmissionForTest(t, srv, 1, time.Millisecond)

	assertCommandDecision(t, srv, "go test ./...", 0, "no-broad-go-test")
}

func TestEvaluateHook_BlocksCopilotVSCodeReplaceStringNewString(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), emdashDaemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	dash := string(rune(0x2014))
	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"replace_string_in_file","tool_input":{"filePath":"/tmp/page.zig","oldString":"old","newString":"new text ` + dash + ` blocked"}}`)
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: rawJSON,
		EnvFingerprint: map[string]string{
			"COPILOT_OTEL_ENABLED": "true",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 2 {
		t.Fatalf("exit_code = %d, want 2", resp.ExitCode)
	}
	if !strings.Contains(string(resp.StderrData), "no-emdashes") {
		t.Fatalf("stderr missing no-emdashes diagnostic: %s", string(resp.StderrData))
	}
}

func TestEvaluateHook_BlocksCopilotVSCodeMultiReplaceNewString(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), emdashDaemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	dash := string(rune(0x2014))
	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"multi_replace_string_in_file","tool_input":{"replacements":[{"filePath":"/tmp/page.zig","oldString":"old","newString":"clean"},{"filePath":"/tmp/list.zig","oldString":"old","newString":"new text ` + dash + ` blocked"}]}}`)
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: rawJSON,
		EnvFingerprint: map[string]string{
			"COPILOT_OTEL_ENABLED": "true",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 2 {
		t.Fatalf("exit_code = %d, want 2", resp.ExitCode)
	}
	if !strings.Contains(string(resp.StderrData), "no-emdashes") {
		t.Fatalf("stderr missing no-emdashes diagnostic: %s", string(resp.StderrData))
	}
}

func TestEvaluateHook_CopilotStopTranscriptAssistantTextIsEvaluated(t *testing.T) {
	setDaemonTestDirs(t)
	dir := t.TempDir()
	transcript := dir + "/copilot.jsonl"
	dash := string(rune(0x2014))
	lines := strings.Join([]string{
		`{"type":"assistant.message","data":{"content":"Clean response."}}`,
		`{"type":"assistant.message","data":{"content":"Final response ` + dash + ` blocked."}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcript, []byte(lines), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	srv, err := New(newDiscardLogger(), emdashDaemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"Stop","transcript_path":"` + transcript + `","stop_hook_active":false}`)
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: rawJSON,
		EnvFingerprint: map[string]string{
			"COPILOT_OTEL_ENABLED": "true",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != 0 {
		t.Fatalf("exit_code = %d, want 0 because Copilot Stop is not blockable", resp.ExitCode)
	}
	if !strings.Contains(string(resp.StdoutData), "{}") {
		t.Fatalf("stdout = %q, want Claude allow response", string(resp.StdoutData))
	}
}

func TestEvaluateHook_CodexStopBlockingRuleDowngradesToAudit(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := codexStopAuditDaemonTestConfig(t)
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"Stop","turn_id":"t1","cwd":"/repo","stop_hook_active":false,"last_assistant_message":"this--is ugly"}`)
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:        rawJSON,
		ProviderHint:   "codex",
		Cwd:            "/repo",
		EnvFingerprint: map[string]string{"CODEX_THREAD_ID": "test-thread"},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if string(resp.StdoutData) != "{}\n" {
		t.Fatalf("stdout = %q, want allow response", string(resp.StdoutData))
	}
	waitForNoPendingIntake(t, srv)
	waitForAuditMessages(t, cfg, "hook.audit_violation", "hook.allowed")
	events, _, err := audit.Query(cfg, audit.QueryFilter{Limit: 20})
	if err != nil {
		t.Fatalf("audit.Query: %v", err)
	}
	for _, event := range events {
		if event.Message == "hook.blocked" {
			t.Fatalf("unexpected hook.blocked event: %+v", event)
		}
	}
}

func TestStatusReportsProcessMetadata(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	resp, err := srv.Status(context.Background(), &daemonpb.StatusRequest{})
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if resp.Pid != int64(os.Getpid()) {
		t.Fatalf("pid = %d, want %d", resp.Pid, os.Getpid())
	}
	if resp.ExecutablePath == "" || resp.SocketPath == "" {
		t.Fatalf("status missing metadata: %+v", resp)
	}
}

func TestReloadConfigValidSwap(t *testing.T) {
	setDaemonTestDirs(t)
	configPath := config.Path()
	writeConfig(t, configPath, `
[audit]
enabled = false

[[rules]]
name = "block-alpha"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "alpha blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	assertCommandDecision(t, srv, "alpha", 0, "block-alpha")
	assertCommandDecision(t, srv, "beta", 0, "")

	writeConfig(t, configPath, `
[audit]
enabled = false

[[rules]]
name = "block-beta"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "beta"
action = "block"
violation_message = "beta blocked"
`)
	if err := srv.reloadConfig(context.Background()); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}

	assertCommandDecision(t, srv, "alpha", 0, "")
	assertCommandDecision(t, srv, "beta", 0, "block-beta")
}

func TestReloadConfigInvalidKeepsPreviousConfig(t *testing.T) {
	setDaemonTestDirs(t)
	configPath := config.Path()
	writeConfig(t, configPath, `
[audit]
enabled = false

[[rules]]
name = "block-alpha"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "alpha blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	writeConfig(t, configPath, `
[audit]
enabled = false

[[rules]]
name = "invalid-regex"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "["
action = "block"
violation_message = "invalid"
`)
	if err := srv.reloadConfig(context.Background()); err == nil {
		t.Fatal("reloadConfig succeeded, want error")
	}

	assertCommandDecision(t, srv, "alpha", 0, "block-alpha")
	assertCommandDecision(t, srv, "beta", 0, "")
}

func TestReloadConfigMissingFileKeepsPreviousConfig(t *testing.T) {
	setDaemonTestDirs(t)
	configPath := config.Path()
	writeConfig(t, configPath, `
[audit]
enabled = false

[[rules]]
name = "block-alpha"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "alpha blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	if err := os.Remove(configPath); err != nil {
		t.Fatalf("remove config: %v", err)
	}
	if err := srv.reloadConfig(context.Background()); err == nil {
		t.Fatal("reloadConfig succeeded, want error")
	}

	assertCommandDecision(t, srv, "alpha", 0, "block-alpha")
	assertCommandDecision(t, srv, "beta", 0, "")
}

func writeConfig(t *testing.T, path string, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func assertCommandDecision(t *testing.T, srv *Server, command string, exitCode int32, ruleName string) {
	t.Helper()
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"` + command + `"}}`),
		ProviderHint: "codex",
		Cwd:          t.TempDir(),
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "test-thread",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if resp.ExitCode != exitCode {
		t.Fatalf("exit_code = %d, want %d", resp.ExitCode, exitCode)
	}
	stdout := string(resp.StdoutData)
	if ruleName == "" {
		if strings.Contains(stdout, `"permissionDecision":"deny"`) {
			t.Fatalf("stdout has deny response: %s", stdout)
		}
		return
	}
	if !strings.Contains(stdout, `"permissionDecision":"deny"`) || !strings.Contains(stdout, ruleName) {
		t.Fatalf("stdout missing deny response for %q: %s", ruleName, stdout)
	}
}

func setEvaluateAdmissionForTest(t testing.TB, srv *Server, concurrency int, wait time.Duration) *runtimeSnapshot {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	snapshot.evaluateSlots = make(chan struct{}, concurrency)
	snapshot.evaluateQueueWait = wait
	return snapshot
}

type failingIntakeStore struct{}

func (failingIntakeStore) Append(context.Context, intake.Record) (intake.AppendResult, error) {
	return intake.AppendResult{}, errors.New("append failed")
}

func (failingIntakeStore) Get(context.Context, string) (intake.Record, error) {
	return intake.Record{}, errors.New("get failed")
}

func (failingIntakeStore) MarkDeferredPending(context.Context, string) error {
	return errors.New("mark pending failed")
}

func (failingIntakeStore) MarkDeferredComplete(context.Context, string) error {
	return errors.New("mark complete failed")
}

func (failingIntakeStore) ReplayPending(context.Context, func(intake.Record) error) error {
	return errors.New("replay failed")
}

func (failingIntakeStore) ListPending(context.Context) ([]string, error) {
	return nil, errors.New("list failed")
}

func (failingIntakeStore) UpdateHotEvalLatency(context.Context, string, int64) error {
	return errors.New("update latency failed")
}

func (failingIntakeStore) Close() error {
	return nil
}

func replaceIntakeStoreForTest(t testing.TB, srv *Server, store intakeStore) {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	snapshot.intakeStore = store
}

func replaceDeferredProcessorForTest(t testing.TB, srv *Server, queueLimit int, workers int) {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	if snapshot.deferredProcessor != nil {
		snapshot.deferredProcessor.Close()
	}
	snapshot.deferredProcessor = newDeferredProcessor(
		context.Background(),
		snapshot.intakeStore,
		nil,
		snapshot.cfg,
		queueLimit,
		workers,
		newDiscardLogger(),
	)
}

func fillDeferredProcessorQueue(t testing.TB, srv *Server) {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil || snapshot.deferredProcessor == nil {
		t.Fatal("deferred processor is nil")
	}
	snapshot.deferredProcessor.events <- "occupied"
}

func setHotEvaluatorForTest(t testing.TB, srv *Server, evaluator func(context.Context, []byte, *config.Config, hook.System, func(string) string, string) hook.HotEvaluation) {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	snapshot.hotEvaluate = evaluator
}

func auditOnlyDaemonTestConfig(t testing.TB) *config.Config {
	t.Helper()
	re := regex.MustCompile(`echo ok`)
	rule := config.NewSimpleRule(
		"audit-echo-ok",
		`echo ok`,
		re,
		[]string{"PreToolUse"},
		[]string{"tool_input.command"},
		"block",
		"Record echo usage.",
	)
	rule.Action = config.ActionAudit
	rule.AuditOnly = true
	return &config.Config{
		Audit: config.Audit{Enabled: boolPtr(true)},
		Rules: []config.Rule{rule},
	}
}

func mixedSyncDeferredDaemonTestConfig(t testing.TB) *config.Config {
	t.Helper()
	blockRe := regex.MustCompile(`go test \./\.\.\.`)
	blockRule := config.NewSimpleRule(
		"no-broad-go-test",
		`go test \./\.\.\.`,
		blockRe,
		[]string{"PreToolUse"},
		[]string{"tool_input.command"},
		"block",
		"Use make test for full project runs.",
	)
	auditRe := regex.MustCompile(`go test`)
	auditRule := config.NewSimpleRule(
		"audit-go-test",
		`go test`,
		auditRe,
		[]string{"PreToolUse"},
		[]string{"tool_input.command"},
		"block",
		"Record go test usage.",
	)
	auditRule.Action = config.ActionAudit
	auditRule.AuditOnly = true
	return &config.Config{
		Audit: config.Audit{Enabled: boolPtr(true)},
		Rules: []config.Rule{blockRule, auditRule},
	}
}

func waitForAuditMessages(t testing.TB, cfg *config.Config, messages ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _, err := audit.Query(cfg, audit.QueryFilter{Limit: 50})
		if err == nil {
			found := make(map[string]bool, len(messages))
			for _, event := range events {
				found[event.Message] = true
			}
			allFound := true
			for _, message := range messages {
				if !found[message] {
					allFound = false
					break
				}
			}
			if allFound {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for audit messages %v", messages)
}

func waitForNoPendingIntake(t testing.TB, srv *Server) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := srv.runtime.Load()
		if snapshot == nil {
			t.Fatal("runtime snapshot is nil")
		}
		pending, err := snapshot.intakeStore.ListPending(context.Background())
		if err != nil {
			t.Fatalf("ListPending: %v", err)
		}
		if len(pending) == 0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("timed out waiting for pending intake records to complete")
}
