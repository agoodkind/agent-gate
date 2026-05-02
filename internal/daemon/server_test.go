package daemon

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

func boolPtr(v bool) *bool { return &v }

func daemonTestConfig(t *testing.T) *config.Config {
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

func TestEvaluateHook_DaemonOwnsEnforcement(t *testing.T) {
	srv, err := New(slog.Default(), daemonTestConfig(t))
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
	srv, err := New(slog.Default(), daemonTestConfig(t))
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

func TestStatusReportsProcessMetadata(t *testing.T) {
	srv, err := New(slog.Default(), daemonTestConfig(t))
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
