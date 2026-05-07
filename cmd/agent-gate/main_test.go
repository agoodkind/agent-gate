package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
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
