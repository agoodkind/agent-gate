package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestAuditRotationWaitsForAdmittedEvaluation(t *testing.T) {
	setDaemonTestDirs(t)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 9, 9, 23, 59, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	server, err := newServer(context.Background(), newDiscardLogger(), daemonTestConfig(t), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	old := server.runtime.Load()
	entered := make(chan struct{})
	release := make(chan struct{})
	setHotEvaluatorForTest(t, server, func(ctx context.Context, input hook.EvaluationInput, cfg *config.Config, getenv func(string) string, eventID string) hook.HotEvaluation {
		close(entered)
		<-release
		return defaultHotEvaluate(ctx, input, cfg, getenv, eventID)
	})
	request := &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"rotation","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
	}
	evaluated := make(chan error, 1)
	go func() {
		_, err := server.EvaluateHook(context.Background(), request)
		evaluated <- err
	}()
	<-entered
	clock.Add(int64(24 * time.Hour / time.Second))
	rotated := make(chan error, 1)
	go func() { rotated <- server.reconcileAuditStorage(context.Background(), now()) }()
	if err := old.bucketHandle.Database.PingContext(context.Background()); err != nil {
		t.Errorf("admitted request lost its database: %v", err)
	}
	select {
	case err := <-rotated:
		t.Errorf("rotation returned before admitted request ended: %v", err)
	default:
	}
	close(release)
	if err := <-evaluated; err != nil {
		t.Fatal(err)
	}
	if err := <-rotated; err != nil {
		t.Fatal(err)
	}
	if current := server.runtime.Load(); current.bucket.ID == old.bucket.ID {
		t.Fatal("rotation kept the old bucket")
	}
	if _, err := server.EvaluateHook(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := old.bucketHandle.Database.PingContext(context.Background()); err == nil {
		t.Fatal("rotation retained the old owning handle")
	}
}

func TestAuditSchedulerDetectsSleepWithManualTimer(t *testing.T) {
	setDaemonTestDirs(t)
	var clock atomic.Int64
	clock.Store(time.Now().Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	server, err := newServer(t.Context(), newDiscardLogger(), daemonTestConfig(t), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	ticks := make(chan time.Time)
	server.auditTicks = ticks
	old := server.runtime.Load().bucket
	server.StartAuditScheduler(t.Context())
	clock.Add(int64(8 * 24 * time.Hour / time.Second))
	select {
	case ticks <- now():
	case <-time.After(time.Second):
		t.Fatal("scheduler did not receive timer")
	}
	waitRuntimeCondition(t, func() bool { return server.runtime.Load().bucket.ID != old.ID })
	waitRuntimeCondition(t, func() bool { _, err := os.Stat(old.Path); return errors.Is(err, os.ErrNotExist) })
}

func TestAuditStorageStartupRetriesBeforeReadiness(t *testing.T) {
	for _, cancelStartup := range []bool{false, true} {
		t.Run(map[bool]string{false: "repair", true: "cancel"}[cancelStartup], func(t *testing.T) {
			setDaemonTestDirs(t)
			cfg := daemonTestConfig(t)
			blocked := filepath.Join(t.TempDir(), "blocked")
			if err := os.WriteFile(blocked, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.Audit.Outputs.SQLite.Path = filepath.Join(blocked, "audit.db")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			waited := make(chan time.Duration, 1)
			resume := make(chan struct{})
			wait := func(ctx context.Context, delay time.Duration) error {
				waited <- delay
				select {
				case <-resume:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			type result struct {
				server *Server
				err    error
			}
			ready := make(chan result, 1)
			go func() {
				server, err := newServerWithStorageWait(ctx, newDiscardLogger(), cfg, time.Now, wait)
				ready <- result{server, err}
			}()
			select {
			case delay := <-waited:
				if delay != time.Second {
					t.Errorf("initial retry = %v", delay)
				}
			case <-time.After(time.Second):
				t.Fatal("startup did not retry")
			}
			select {
			case <-ready:
				t.Fatal("startup exposed uninitialized storage")
			default:
			}
			if cancelStartup {
				cancel()
			} else {
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
				close(resume)
			}
			select {
			case got := <-ready:
				if cancelStartup {
					if !errors.Is(got.err, context.Canceled) {
						t.Fatalf("canceled startup = %v", got.err)
					}
					return
				}
				if got.err != nil {
					t.Fatal(got.err)
				}
				defer got.server.Close()
				if err := got.server.runtime.Load().bucketHandle.Database.PingContext(t.Context()); err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup retry did not finish")
			}
		})
	}
}

func TestAuditStorageRetrySchedule(t *testing.T) {
	var delays []time.Duration
	attempts := 0
	err := retryAuditStorage(t.Context(), newDiscardLogger(), func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }, func() error {
		attempts++
		if attempts <= 7 {
			return errors.New("injected storage error")
		}
		return nil
	})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	if err != nil || !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays = %v, %v", delays, err)
	}
}

func TestAuditReloadCanonicalIdentityAndPolicyCut(t *testing.T) {
	setDaemonTestDirs(t)
	base := filepath.Join(t.TempDir(), "audit.db")
	cfg := daemonTestConfig(t)
	cfg.Audit.Outputs.SQLite.Path = base
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	if _, err := server.EvaluateHook(t.Context(), blockingLedgerRequest(t)); err != nil {
		t.Fatal(err)
	}
	old := server.runtime.Load().bucket
	link := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Dir(base), link); err != nil {
		t.Fatal(err)
	}
	absolute, err := filepath.Abs(base)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, absolute)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{relative, filepath.Join(link, "audit.db")} {
		reloadAuditTestConfig(t, server, path, "24h", 7)
		if server.runtime.Load().bucket != old {
			t.Fatalf("equivalent path replaced bucket: %+v", server.runtime.Load().bucket)
		}
		assertRuntimeReceipts(t, server, 1)
	}
	reloadAuditTestConfig(t, server, base, "86400s", 7)
	assertRuntimeReceipts(t, server, 1)
	reloadAuditTestConfig(t, server, base, "24h", 3)
	assertRuntimeReceipts(t, server, 0)
	if _, err := server.EvaluateHook(t.Context(), blockingLedgerRequest(t)); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "changed.db")
	reloadAuditTestConfig(t, server, other, "24h", 3)
	assertRuntimeReceipts(t, server, 0)
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old family survived changed path: %v", err)
	}
}

func TestAuditPolicyCutRetriesHeldReaderWithoutRestoringHistory(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := newServer(t.Context(), newDiscardLogger(), daemonTestConfig(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	old := server.runtime.Load()
	readers, err := server.catalog.Read(t.Context(), old.cfg.AuditStoragePolicy().Rotation(), server.now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readers.Close() }()
	waited := make(chan struct{}, 1)
	resume := make(chan struct{})
	server.retryWait = func(ctx context.Context, _ time.Duration) error {
		waited <- struct{}{}
		select {
		case <-resume:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	server.configPath = filepath.Join(t.TempDir(), "candidate.toml")
	writeAuditTestConfig(t, server.configPath, old.cfg.AuditSQLitePath(), "24h", 3)
	finished := make(chan error, 1)
	go func() { finished <- server.reloadConfig(t.Context()) }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("policy cut did not retry held reader")
	}
	if server.runtime.Load() != nil {
		t.Error("failed cut restored old runtime")
	}
	data, err := os.ReadFile(old.cfg.AuditCatalogOptions().StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var state auditstorage.CatalogState
	if err := json.Unmarshal(data, &state); err != nil || !state.ResetPending {
		t.Fatalf("pending state = %+v, %v", state, err)
	}
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("policy cut retry did not finish")
	}
	assertRuntimeReceipts(t, server, 0)
}

func TestAuditRotationFailureKeepsWriter(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := newServer(t.Context(), newDiscardLogger(), daemonTestConfig(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	old := server.runtime.Load()
	policy := old.cfg.AuditStoragePolicy().Rotation()
	now := old.bucket.Start.Add(policy.Interval)
	bucket, err := server.catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bucket.Path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bucket.Path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := server.reconcileAuditStorage(t.Context(), now); err == nil {
		t.Fatal("rotation opened a corrupt writer")
	}
	if server.runtime.Load() != old {
		t.Fatal("failed rotation replaced old runtime")
	}
	if _, err := server.EvaluateHook(t.Context(), blockingLedgerRequest(t)); err != nil {
		t.Fatal(err)
	}
	assertRuntimeReceipts(t, server, 1)
	if err := os.Remove(bucket.Path); err != nil {
		t.Fatal(err)
	}
	if err := server.reconcileAuditStorage(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if server.runtime.Load().bucket.ID == old.bucket.ID {
		t.Fatal("repair did not rotate")
	}
}

func TestAuditUnusableStartupKeepsHistoryHiddenUntilValidReload(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
		t.Fatal(err)
	}
	old := server.runtime.Load().bucket
	server.Close()
	path := filepath.Join(t.TempDir(), "invalid.toml")
	writeConfig(t, path, "[audit.storage\n")
	broken, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatal(err)
	}
	server, err = New(t.Context(), newDiscardLogger(), broken)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if server.catalog != nil || server.runtime.Load().bucketHandle != nil {
		t.Fatal("unusable configuration opened audit history")
	}
	response, err := server.EvaluateHook(t.Context(), replayRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertSaysUnevaluated(t, response, hook.FailOpenReasonConfigUnusable)
	if _, err := os.Stat(old.Path); err != nil {
		t.Fatal(err)
	}
	reloadAuditTestConfig(t, server, cfg.AuditSQLitePath(), "24h", 7)
	assertRuntimeReceipts(t, server, 1)
}

func TestAuditCloseCancelsInterruptedPolicyCut(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := newServer(t.Context(), newDiscardLogger(), daemonTestConfig(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	old := server.runtime.Load()
	readers, err := server.catalog.Read(t.Context(), old.cfg.AuditStoragePolicy().Rotation(), server.now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readers.Close() }()
	waited := make(chan struct{}, 1)
	server.retryWait = func(ctx context.Context, _ time.Duration) error { waited <- struct{}{}; <-ctx.Done(); return ctx.Err() }
	server.configPath = filepath.Join(t.TempDir(), "candidate.toml")
	writeAuditTestConfig(t, server.configPath, old.cfg.AuditSQLitePath(), "24h", 2)
	reloaded := make(chan error, 1)
	go func() { reloaded <- server.reloadConfig(t.Context()) }()
	select {
	case <-waited:
	case <-time.After(time.Second):
		t.Fatal("reset did not enter retry")
	}
	closed := make(chan struct{})
	go func() { server.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("close waited on canceled reset")
	}
	if err := <-reloaded; !errors.Is(err, context.Canceled) {
		t.Fatalf("reload cancellation = %v", err)
	}
	if server.runtime.Load() != nil {
		t.Fatal("canceled cut restored history")
	}
}

func TestAuditCorruptRetainedHistoryDoesNotAbortStartup(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	before := time.Now().Add(-24 * time.Hour)
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, func() time.Time { return before })
	if err != nil {
		t.Fatal(err)
	}
	old := server.runtime.Load().bucket
	server.Close()
	if err := os.WriteFile(old.Path, []byte("corrupt retained history"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err = New(t.Context(), newDiscardLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := server.dispatchRetainedReplay(t.Context(), time.Now()); err == nil {
		t.Fatal("corrupt retained history was hidden")
	}
	if _, err := server.EvaluateHook(t.Context(), replayRequest()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeReceipts(t, server, 1)
}

func writeAuditTestConfig(t *testing.T, path, base, interval string, retained int) {
	t.Helper()
	body := fmt.Sprintf("[audit]\nenabled = false\n[audit.outputs.sqlite]\npath = %q\n[audit.storage]\nbucket_interval = %q\nretention_buckets = %d\n", base, interval, retained)
	writeConfig(t, path, body)
}

func reloadAuditTestConfig(t *testing.T, server *Server, base, interval string, retained int) {
	t.Helper()
	server.configPath = filepath.Join(t.TempDir(), "candidate.toml")
	writeAuditTestConfig(t, server.configPath, base, interval, retained)
	if err := server.reloadConfig(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeReceipts(t *testing.T, server *Server, want int) {
	t.Helper()
	var count int
	if err := server.runtime.Load().bucketHandle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "count_runtime_receipts.sql")).Scan(&count); err != nil || count != want {
		t.Fatalf("receipt count = %d, want %d; %v", count, want, err)
	}
}

func waitRuntimeCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-deadline.C:
			t.Fatal("runtime condition did not settle")
		case <-ticker.C:
		}
	}
}
