package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/api/inferencepb"
	"goodkind.io/agent-gate/internal/config"
)

func BenchmarkAuditTrace(b *testing.B) {
	for range b.N {
		fake := newDeferredInferenceFake(`{"decision":"block"}`)
		endpoint := startAuditTraceInferenceServer(b, fake)
		cfg := auditTraceConfig(b, endpoint)
		databasePath := cfg.AuditSQLitePath()
		srv := newBenchmarkServer(b, cfg)
		requests := auditPerformanceTrace()

		b.ResetTimer()
		for _, request := range requests {
			response, err := srv.EvaluateHook(context.Background(), request)
			if err != nil {
				b.Fatalf("EvaluateHook: %v", err)
			}
			if response.GetExitCode() != 0 {
				b.Fatalf("exit_code = %d, want 0", response.GetExitCode())
			}
		}
		b.StopTimer()
		flushAuditTrace(b, srv)
		srv.Close()
		if got := fake.callCount(); got != auditTraceDeferredRequests {
			b.Fatalf("inference calls = %d, want %d", got, auditTraceDeferredRequests)
		}
		b.ReportMetric(float64(auditTraceStoredBytes(b, databasePath)), "stored-bytes")
	}
}

func flushAuditTrace(b *testing.B, srv *Server) {
	b.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil || snapshot.deferredProcessor == nil {
		b.Fatal("runtime snapshot has no deferred processor")
	}
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		pending, err := snapshot.intakeStore.ListPending(context.Background())
		if err != nil {
			b.Fatalf("ListPending: %v", err)
		}
		pendingAudit, err := snapshot.intakeStore.ListPendingDeferredAudit(
			context.Background(),
			0,
		)
		if err != nil {
			b.Fatalf("ListPendingDeferredAudit: %v", err)
		}
		if len(pending) == 0 && len(pendingAudit) == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Fatal("timed out flushing audit trace")
}

func startAuditTraceInferenceServer(
	b *testing.B,
	fake *deferredInferenceFake,
) string {
	b.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("Listen: %v", err)
	}
	server := grpc.NewServer()
	inferencepb.RegisterInferenceServer(server, fake)
	go func() {
		_ = server.Serve(listener)
	}()
	b.Cleanup(server.Stop)
	return listener.Addr().String()
}

func auditTraceConfig(b *testing.B, endpoint string) *config.Config {
	b.Helper()
	directory := b.TempDir()
	path := filepath.Join(directory, "config.toml")
	body := `
[audit]
enabled = true

[audit.outputs.sqlite]
path = "` + filepath.Join(directory, "audit.db") + `"

[[rules]]
name = "performance-block"
events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "performance-block"
action = "block"
violation_message = "blocked"

[[rules]]
name = "performance-deferred"
events = ["PreToolUse"]
action = "audit"
violation_message = "deferred"
[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "performance-deferred"
[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "performance-inference"
prompt = "Classify"
input_field = "tool_input.command"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
cache_ttl_ms = 0
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		b.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		b.Fatalf("LoadExisting: %v", err)
	}
	cfg.Performance.Hook.DeferredQueueLimit = auditTraceAllowRequests +
		auditTraceBlockRequests + auditTraceDeferredRequests
	cfg.Performance.Hook.DeferredWorkers = 1
	cfg.Performance.Limits.AuditQueueLimit = 5000
	return cfg
}

func auditTraceStoredBytes(b *testing.B, databasePath string) int64 {
	b.Helper()
	var total int64
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(databasePath + suffix)
		if err == nil {
			total += info.Size()
			continue
		}
		if !os.IsNotExist(err) {
			b.Fatalf("Stat %s: %v", databasePath+suffix, err)
		}
	}
	return total
}

func BenchmarkEvaluateHookAllowParallel(b *testing.B) {
	srv := newBenchmarkServer(b, daemonTestConfig(b))
	cwd := b.TempDir()
	req := &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"bench","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
		Cwd:          cwd,
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "bench-thread",
		},
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := srv.EvaluateHook(context.Background(), req)
			if err != nil {
				b.Fatalf("EvaluateHook: %v", err)
			}
			if resp.ExitCode != 0 {
				b.Fatalf("exit_code = %d, want 0", resp.ExitCode)
			}
		}
	})
}

func BenchmarkEvaluateHookBlockParallel(b *testing.B) {
	srv := newBenchmarkServer(b, daemonTestConfig(b))
	cwd := b.TempDir()
	req := &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"bench","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex",
		Cwd:          cwd,
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "bench-thread",
		},
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := srv.EvaluateHook(context.Background(), req)
			if err != nil {
				b.Fatalf("EvaluateHook: %v", err)
			}
			if resp.ExitCode != 0 || len(resp.StdoutData) == 0 {
				b.Fatalf("unexpected block response: exit=%d stdout=%q", resp.ExitCode, string(resp.StdoutData))
			}
		}
	})
}

func BenchmarkEvaluateHookAuditEnabledParallel(b *testing.B) {
	cfg := daemonTestConfig(b)
	cfg.Audit = config.Audit{
		Enabled: boolPtr(true),
		Level:   "",
		Outputs: config.AuditOutput{
			SQLite: config.AuditSQLiteOutput{Path: filepath.Join(b.TempDir(), "sqlite", "audit.db")},
		},
	}
	srv := newBenchmarkServer(b, cfg)
	cwd := b.TempDir()
	req := &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"bench","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
		Cwd:          cwd,
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "bench-thread",
		},
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := srv.EvaluateHook(context.Background(), req)
			if err != nil {
				b.Fatalf("EvaluateHook: %v", err)
			}
			if resp.ExitCode != 0 {
				b.Fatalf("exit_code = %d, want 0", resp.ExitCode)
			}
		}
	})
}

func BenchmarkEvaluateHookFullDeferredQueueParallel(b *testing.B) {
	srv := newBenchmarkServer(b, daemonTestConfig(b))
	replaceDeferredProcessorForTest(b, srv, 1, 0)
	fillDeferredProcessorQueue(b, srv)
	cwd := b.TempDir()
	req := &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"bench","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		ProviderHint: "codex",
		Cwd:          cwd,
		EnvFingerprint: map[string]string{
			"CODEX_THREAD_ID": "bench-thread",
		},
	}

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := srv.EvaluateHook(context.Background(), req)
			if err != nil {
				b.Fatalf("EvaluateHook: %v", err)
			}
			if resp.ExitCode != 0 {
				b.Fatalf("exit_code = %d, want 0", resp.ExitCode)
			}
		}
	})
}

func newBenchmarkServer(b *testing.B, cfg *config.Config) *Server {
	b.Helper()
	dir := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	b.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	b.Cleanup(srv.Close)
	return srv
}
