package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
)

func TestEvaluateHookCommitFailureDoesNotPublishTemporalResponse(t *testing.T) {
	setDaemonTestDirs(t)
	const privatePrompt = "private prompt contents"
	const privateResponse = "private response contents"
	cfg := temporalDaemonConfig(t, `
[[rules]]
name = "stop-validator"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server, err := New(logger, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	closeServer := sync.OnceFunc(server.Close)
	defer closeServer()
	runner := &daemonSequenceRunner{
		mu: sync.Mutex{},
		responses: []execconcern.RunResult{
			{ExitCode: 0, Stdout: privateResponse},
			{ExitCode: 1},
		},
	}
	snapshot := server.runtime.Load()
	snapshot.execRuntime = rules.NewExecRuntimeWithCache(
		runner,
		logger,
		server.hotKV,
	)
	if !snapshot.execRuntime.ObserveUserPrompt(
		hook.SystemCursor.String(),
		rules.FieldSet{ConversationID: "c1"},
		1,
		privatePrompt,
	) {
		t.Fatal("ObserveUserPrompt did not store private prompt")
	}
	failingRecorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil,
		hotErr: errors.New("atomic hot commit unavailable"), started: nil, release: nil,
	}
	snapshot.evaluationRecorder = failingRecorder
	request := &daemonpb.EvaluateHookRequest{
		RawJson: []byte(
			`{"hook_event_name":"stop","conversation_id":"c1","status":"completed"}`,
		),
		ProviderHint: hook.SystemCursor.String(),
	}

	first, err := server.EvaluateHook(context.Background(), request)
	if err != nil {
		t.Fatalf("first EvaluateHook: %v", err)
	}
	if first.ExitCode != 0 || len(first.StdoutData) != 0 || len(first.StderrData) != 0 {
		t.Fatalf("failed commit response = %+v", first)
	}

	successfulRecorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, hotErr: nil,
		started: nil, release: nil,
	}
	snapshot.evaluationRecorder = successfulRecorder
	if _, err = server.EvaluateHook(context.Background(), request); err != nil {
		t.Fatalf("second EvaluateHook: %v", err)
	}
	closeServer()

	inputs := runner.Inputs()
	if len(inputs) != 2 {
		t.Fatalf("exec inputs = %d, want two", len(inputs))
	}
	assertDaemonMatchedField(t, inputs[1], "last_user_message", privatePrompt, true)
	assertDaemonMatchedField(t, inputs[1], "last_response_output", "", false)

	recordBytes, err := json.Marshal(append(
		failingRecorder.snapshot(),
		successfulRecorder.snapshot()...,
	))
	if err != nil {
		t.Fatalf("Marshal records: %v", err)
	}
	for _, confidentialValue := range []string{privatePrompt, privateResponse} {
		if strings.Contains(logs.String(), confidentialValue) {
			t.Fatalf("operational log contains temporal content %q", confidentialValue)
		}
		if strings.Contains(string(recordBytes), confidentialValue) {
			t.Fatalf("evaluation record contains temporal content %q", confidentialValue)
		}
	}
}
