package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
)

func BenchmarkEvaluateHookAllowParallel(b *testing.B) {
	dir := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	b.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
	srv, err := New(newDiscardLogger(), daemonTestConfig(b))
	if err != nil {
		b.Fatalf("New: %v", err)
	}
	defer srv.Close()

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
