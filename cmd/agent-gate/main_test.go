package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

type readError struct{}

func (readError) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

type fakeHookClient struct {
	response *daemonpb.EvaluateHookResponse
	err      error
}

func (client fakeHookClient) EvaluateHook(_ []byte, _ string, _ string, _ []string, _ map[string]string) (*daemonpb.EvaluateHookResponse, error) {
	if client.err != nil {
		return nil, client.err
	}
	return client.response, nil
}

func (client fakeHookClient) Close() error {
	return nil
}

func TestRunHookFailOpenOnStdinReadFailure(t *testing.T) {
	runtime, stdout, stderr := testHookRuntime(readError{}, nil)

	exitCode := runHookWithRuntime(hook.SystemCodex, runtime)

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if got := stdout.String(); got != "{}\n" {
		t.Fatalf("stdout = %q, want Codex allow", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHookFailOpenOnDaemonUnavailable(t *testing.T) {
	connect := func(context.Context) (hookClient, error) {
		return nil, errors.New("missing socket")
	}
	runtime, stdout, stderr := testHookRuntime(strings.NewReader(`{"hook_event_name":"preToolUse"}`), connect)

	exitCode := runHookWithRuntime(hook.SystemCursor, runtime)

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if got := stdout.String(); got != "{\"permission\":\"allow\"}\n" {
		t.Fatalf("stdout = %q, want Cursor allow", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHookFailOpenOnRPCFailure(t *testing.T) {
	connect := func(context.Context) (hookClient, error) {
		return fakeHookClient{err: errors.New("rpc failed")}, nil
	}
	runtime, stdout, stderr := testHookRuntime(strings.NewReader(`{"hook_event_name":"BeforeTool"}`), connect)

	exitCode := runHookWithRuntime(hook.SystemGemini, runtime)

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if got := stdout.String(); got != "{}\n" {
		t.Fatalf("stdout = %q, want Gemini allow", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHookFailOpenOnHookPanic(t *testing.T) {
	connect := func(context.Context) (hookClient, error) {
		panic("test panic")
	}
	runtime, stdout, stderr := testHookRuntime(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), connect)

	exitCode := runHookWithRuntime(hook.SystemClaude, runtime)

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if got := stdout.String(); got != "{}\n" {
		t.Fatalf("stdout = %q, want Claude allow", got)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunHookMirrorsDaemonBlockResponse(t *testing.T) {
	connect := func(context.Context) (hookClient, error) {
		return fakeHookClient{
			response: &daemonpb.EvaluateHookResponse{
				ExitCode:   2,
				StdoutData: []byte("{}\n"),
				StderrData: []byte("blocked\n"),
			},
		}, nil
	}
	runtime, stdout, stderr := testHookRuntime(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), connect)

	exitCode := runHookWithRuntime(hook.SystemClaude, runtime)

	if exitCode != 2 {
		t.Fatalf("exitCode = %d, want daemon exit code 2", exitCode)
	}
	if got := stdout.String(); got != "{}\n" {
		t.Fatalf("stdout = %q, want daemon stdout", got)
	}
	if got := stderr.String(); got != "blocked\n" {
		t.Fatalf("stderr = %q, want daemon stderr", got)
	}
}

func TestRunQueryUnknownSubcommandFailsClearly(t *testing.T) {
	exitCode, _, stderr := captureRunQuery(t, []string{"bogus"})

	if exitCode != 2 {
		t.Fatalf("exitCode = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr, `unknown subcommand "bogus"`) {
		t.Fatalf("stderr = %q, want unknown subcommand message", stderr)
	}
}

func TestRunQuerySeenAcceptsSharedAndIntakeFilters(t *testing.T) {
	setupQueryEnvironment(t)

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"seen",
		"--system", "claude",
		"--session", "session-1",
		"--event", "PreToolUse",
		"--tool", "Bash",
		"--state", "none",
		"--event-id", "evt_1",
		"--since", "1h",
		"--until", "2099-01-01T00:00:00Z",
		"--limit", "5",
		"--json",
		"--include-normalized",
		"--include-env",
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0; stderr=%q", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("stdout = %q, want empty JSONL for missing intake history", stdout)
	}
	if !strings.Contains(stderr, "no durable seen-event history") {
		t.Fatalf("stderr = %q, want friendly empty history note", stderr)
	}
}

func TestRunQueryDecisionsPreservesAuditQueryBehavior(t *testing.T) {
	setupQueryEnvironment(t)
	logger, err := audit.NewEventLoggerContext(context.Background(), &config.Config{}, nil)
	if err != nil {
		t.Fatalf("NewEventLoggerContext: %v", err)
	}
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", audit.Attrs{
		"system":         audit.NewStringValue("claude"),
		"session_id":     audit.NewStringValue("session-1"),
		"event":          audit.NewStringValue("PreToolUse"),
		"tool_name":      audit.NewStringValue("Bash"),
		"decision":       audit.NewStringValue("block"),
		"blocking_rules": audit.NewStringSliceValue([]string{"use-make-not-go-direct"}),
	})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"decisions",
		"--system", "claude",
		"--event", "PreToolUse",
		"--tool", "Bash",
		"--decision", "block",
		"--rule", "use-make-not-go-direct",
		"--limit", "5",
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0; stderr=%q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "source=sqlite rows=1") {
		t.Fatalf("stdout = %q, want one SQLite-backed audit row", stdout)
	}
	if !strings.Contains(stdout, "use-make-not-go-direct") {
		t.Fatalf("stdout = %q, want matched rule", stdout)
	}
}

func testHookRuntime(stdin io.Reader, connect func(context.Context) (hookClient, error)) (hookRuntime, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	if connect == nil {
		connect = func(context.Context) (hookClient, error) {
			return fakeHookClient{}, nil
		}
	}
	runtime := hookRuntime{
		stdin:   stdin,
		stdout:  stdout,
		stderr:  stderr,
		args:    []string{"agent-gate"},
		connect: connect,
		getwd: func() (string, error) {
			return "/tmp", nil
		},
		env: func() map[string]string {
			return map[string]string{}
		},
	}
	return runtime, stdout, stderr
}

func setupQueryEnvironment(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(dir, "runtime"))
}

func captureRunQuery(t *testing.T, args []string) (int, string, string) {
	t.Helper()
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("CreateTemp stdout: %v", err)
	}
	stderrFile, err := os.CreateTemp(t.TempDir(), "stderr-*")
	if err != nil {
		t.Fatalf("CreateTemp stderr: %v", err)
	}
	originalStdout := os.Stdout
	originalStderr := os.Stderr
	os.Stdout = stdoutFile
	os.Stderr = stderrFile
	defer func() {
		os.Stdout = originalStdout
		os.Stderr = originalStderr
	}()

	exitCode := runQuery(args)
	stdout := readCapturedFile(t, stdoutFile)
	stderr := readCapturedFile(t, stderrFile)
	return exitCode, stdout, stderr
}

func readCapturedFile(t *testing.T, file *os.File) string {
	t.Helper()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek captured file: %v", err)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("ReadAll captured file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close captured file: %v", err)
	}
	return string(data)
}
