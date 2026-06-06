package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
)

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
