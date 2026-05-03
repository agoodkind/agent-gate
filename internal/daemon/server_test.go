package daemon

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

func boolPtr(v bool) *bool { return &v }

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}

func setDaemonTestDirs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
}

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

func emdashDaemonTestConfig(t *testing.T) *config.Config {
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
	configPath := config.ConfigPath()
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
	configPath := config.ConfigPath()
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
	configPath := config.ConfigPath()
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
