package daemon

import (
	"bytes"
	"context"
	"encoding/json"
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
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/regex"
	"goodkind.io/agent-gate/internal/rules"
	execconcern "goodkind.io/agent-gate/internal/rules/concerns/exec"
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

type daemonInputRunner struct {
	mu    sync.Mutex
	input execconcern.Input
}

func (r *daemonInputRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	r.mu.Lock()
	r.input = input
	r.mu.Unlock()
	return execconcern.RunResult{ExitCode: 0}, nil
}

func (r *daemonInputRunner) Input() execconcern.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.input
}

type daemonSequenceRunner struct {
	mu        sync.Mutex
	inputs    []execconcern.Input
	responses []execconcern.RunResult
}

func (r *daemonSequenceRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	index := len(r.inputs) - 1
	if index >= len(r.responses) {
		index = len(r.responses) - 1
	}
	return r.responses[index], nil
}

func (r *daemonSequenceRunner) Inputs() []execconcern.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execconcern.Input(nil), r.inputs...)
}

type cursorTemporalRunner struct {
	mu     sync.Mutex
	inputs []execconcern.Input
}

func (r *cursorTemporalRunner) Run(
	_ context.Context,
	_ []string,
	_ time.Duration,
	stdin []byte,
	_ []string,
) (execconcern.RunResult, error) {
	var input execconcern.Input
	if err := json.Unmarshal(stdin, &input); err != nil {
		return execconcern.RunResult{}, err
	}
	r.mu.Lock()
	r.inputs = append(r.inputs, input)
	r.mu.Unlock()

	lastUserMessage, _ := matchedField(input, "last_user_message")
	lastResponseOutput, _ := matchedField(input, "last_response_output")
	if lastResponseOutput.Available == nil || !*lastResponseOutput.Available {
		return execconcern.RunResult{ExitCode: 0, Stdout: "Continue"}, nil
	}
	if lastUserMessage.Value == lastResponseOutput.Value {
		return execconcern.RunResult{ExitCode: 1}, nil
	}
	return execconcern.RunResult{
		ExitCode: 0,
		Stdout:   "Continue " + lastUserMessage.Value,
	}, nil
}

func (r *cursorTemporalRunner) Inputs() []execconcern.Input {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execconcern.Input(nil), r.inputs...)
}

func matchedField(input execconcern.Input, name string) (execconcern.FieldValue, bool) {
	for _, field := range input.Matched {
		if field.Field == name {
			return field, true
		}
	}
	return execconcern.FieldValue{}, false
}

func temporalDaemonConfig(t testing.TB, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	cfg.Audit.Enabled = boolPtr(false)
	return cfg
}

func TestEvaluateHookObservesCurrentPromptBeforeExec(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := temporalDaemonConfig(t, `
[[rules]]
name = "prompt-validator"
cursor_events = ["beforeSubmitPrompt"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message"]
cache_ttl_ms = 0
`)
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	runner := &daemonInputRunner{mu: sync.Mutex{}}
	server.runtime.Load().execRuntime = rules.NewExecRuntimeWithCache(
		runner,
		newDiscardLogger(),
		server.hotKV,
	)

	_, err = server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: []byte(
			`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
				`"prompt":"current prompt"}`,
		),
		ProviderHint: hook.SystemCursor.String(),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	input := runner.Input()
	if len(input.Matched) != 1 || input.Matched[0].Value != "current prompt" ||
		input.Matched[0].Available == nil || !*input.Matched[0].Available {
		t.Fatalf("exec input = %#v, want current prompt available", input.Matched)
	}
}

func TestEvaluateHookRecordsResponseAfterCommitForNextExec(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := temporalDaemonConfig(t, `
[[rules]]
name = "stop-validator"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_response_output"]
cache_ttl_ms = 0
`)
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	runner := &daemonSequenceRunner{
		mu: sync.Mutex{},
		responses: []execconcern.RunResult{
			{ExitCode: 0, Stdout: "first response"},
			{ExitCode: 1},
		},
	}
	server.runtime.Load().execRuntime = rules.NewExecRuntimeWithCache(
		runner,
		newDiscardLogger(),
		server.hotKV,
	)
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
	if !strings.Contains(string(first.StdoutData), `"followup_message":"first response"`) {
		t.Fatalf(
			"first response = %q; exec inputs = %#v",
			string(first.StdoutData),
			runner.Inputs(),
		)
	}
	_, err = server.EvaluateHook(context.Background(), request)
	if err != nil {
		t.Fatalf("second EvaluateHook: %v", err)
	}

	inputs := runner.Inputs()
	if len(inputs) != 2 {
		t.Fatalf("exec inputs = %d, want 2", len(inputs))
	}
	previous := inputs[1].Matched[0]
	if previous.Value != "first response" || previous.Available == nil || !*previous.Available {
		t.Fatalf("second last_response_output = %#v", previous)
	}
}

func TestEvaluateHookCursorTemporalResponseSequence(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := temporalDaemonConfig(t, `
[[rules]]
name = "stop-validator"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = [
    "last_user_message",
    "last_response_output",
    "response_output",
    "loop_count",
]
cache_ttl_ms = 0
`)
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	runner := &cursorTemporalRunner{mu: sync.Mutex{}, inputs: nil}
	server.runtime.Load().execRuntime = rules.NewExecRuntimeWithCache(
		runner,
		newDiscardLogger(),
		server.hotKV,
	)

	evaluate := func(rawJSON string) *daemonpb.EvaluateHookResponse {
		t.Helper()
		response, evaluateErr := server.EvaluateHook(
			context.Background(),
			&daemonpb.EvaluateHookRequest{
				RawJson:      []byte(rawJSON),
				ProviderHint: hook.SystemCursor.String(),
			},
		)
		if evaluateErr != nil {
			t.Fatalf("EvaluateHook: %v", evaluateErr)
		}
		return response
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"initial request"}`,
	)
	firstStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":0}`,
	)
	if !strings.Contains(string(firstStop.StdoutData), `"followup_message":"Continue"`) {
		t.Fatalf("first stop response = %q", firstStop.StdoutData)
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"Continue"}`,
	)
	repeatedStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":1}`,
	)
	if strings.Contains(string(repeatedStop.StdoutData), "followup_message") {
		t.Fatalf("repeated stop was not suppressed: %q", repeatedStop.StdoutData)
	}

	evaluate(
		`{"hook_event_name":"beforeSubmitPrompt","conversation_id":"c1",` +
			`"prompt":"different request"}`,
	)
	differentStop := evaluate(
		`{"hook_event_name":"stop","conversation_id":"c1",` +
			`"status":"completed","loop_count":2}`,
	)
	if !strings.Contains(
		string(differentStop.StdoutData),
		`"followup_message":"Continue different request"`,
	) {
		t.Fatalf("different stop response = %q", differentStop.StdoutData)
	}

	inputs := runner.Inputs()
	if len(inputs) != 3 {
		t.Fatalf("exec inputs = %d, want three stop evaluations", len(inputs))
	}
	assertDaemonMatchedField(t, inputs[0], "last_user_message", "initial request", true)
	assertDaemonMatchedField(t, inputs[0], "last_response_output", "", false)
	assertDaemonMatchedField(t, inputs[0], "response_output", "configured fallback", true)
	assertDaemonMatchedField(t, inputs[0], "loop_count", "0", true)
	assertDaemonMatchedField(t, inputs[1], "last_user_message", "Continue", true)
	assertDaemonMatchedField(t, inputs[1], "last_response_output", "Continue", true)
	assertDaemonMatchedField(t, inputs[1], "loop_count", "1", true)
	assertDaemonMatchedField(t, inputs[2], "last_user_message", "different request", true)
	assertDaemonMatchedField(t, inputs[2], "last_response_output", "Continue", true)
	assertDaemonMatchedField(t, inputs[2], "loop_count", "2", true)
}

func assertDaemonMatchedField(
	t *testing.T,
	input execconcern.Input,
	name string,
	value string,
	available bool,
) {
	t.Helper()
	field, found := matchedField(input, name)
	if !found || field.Value != value || field.Available == nil ||
		*field.Available != available {
		t.Fatalf(
			"matched field %q = %#v, found=%v, want value=%q available=%v",
			name,
			field,
			found,
			value,
			available,
		)
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

func TestRuntimeSnapshotReplayFailureDoesNotAbortStartup(t *testing.T) {
	setDaemonTestDirs(t)
	originalReplay := replayRuntimeSnapshotPending
	t.Cleanup(func() {
		replayRuntimeSnapshotPending = originalReplay
	})
	// Replay runs in the background so the daemon serves the gate socket immediately.
	// A replay failure is audit backfill, not gate enforcement, so it is logged rather
	// than aborting startup or closing the intake store.
	storeCh := make(chan *sqliteIntakeStore, 1)
	replayRuntimeSnapshotPending = func(
		processor *deferredProcessor,
		_ context.Context,
	) error {
		store, ok := processor.store.(*sqliteIntakeStore)
		if !ok {
			t.Errorf("processor store = %T, want *sqliteIntakeStore", processor.store)
			storeCh <- nil
			return errors.New("replay unavailable")
		}
		storeCh <- store
		return errors.New("replay unavailable")
	}

	snapshot, err := newRuntimeSnapshot(
		context.Background(), daemonTestConfig(t), newDiscardLogger(), nil, nil,
	)
	if err != nil || snapshot == nil {
		t.Fatalf("newRuntimeSnapshot = %+v, %v; want startup to succeed despite replay failure", snapshot, err)
	}
	t.Cleanup(func() {
		snapshot.close(context.Background(), newDiscardLogger())
	})
	var openedStore *sqliteIntakeStore
	select {
	case openedStore = <-storeCh:
	case <-time.After(5 * time.Second):
		t.Fatal("background replay was not invoked")
	}
	if openedStore == nil {
		t.Fatal("replay hook did not capture intake store")
	}
	if err := openedStore.Handle().PingContext(context.Background()); err != nil {
		t.Fatalf("intake store should stay open after a background replay failure: %v", err)
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

	classification := hook.Classify(raw, hook.SystemClaude, nil, nil)
	record, err := buildClassifiedIntakeRecord(
		raw,
		raw,
		classification,
		nil,
	)
	if err != nil {
		t.Fatalf("buildIntakeRecord: %v", err)
	}
	if record.Operation.EffectiveCWD != "" {
		t.Fatalf("EffectiveCWD = %q, want empty for an unresolvable cwd", record.Operation.EffectiveCWD)
	}
}

func TestEvaluateHookPreservesWireInput(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	wireInput := []byte(`{"hook_event_name":"preToolUse","conversation_id":"cursor-wire","cursor_version":"1.0","cwd":"/payload","tool_name":"Shell","tool_input":{"command":"true"}}`)
	requestCWD := filepath.Join(t.TempDir(), "request-cwd")
	response, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: wireInput,
		Cwd:     requestCWD,
		Argv:    []string{"agent-gate"},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", response.ExitCode)
	}

	record := onlyIntakeRecord(t, srv)
	if !bytes.Equal(record.RawPayload, wireInput) {
		t.Fatalf("raw payload changed: got %q want %q", record.RawPayload, wireInput)
	}
	var normalized map[string]json.RawMessage
	if err := json.Unmarshal(record.NormalizedJSON, &normalized); err != nil {
		t.Fatalf("decode normalized payload: %v", err)
	}
	var normalizedCWD string
	if err := json.Unmarshal(normalized["cwd"], &normalizedCWD); err != nil {
		t.Fatalf("decode normalized cwd: %v", err)
	}
	if normalizedCWD != requestCWD {
		t.Fatalf("normalized cwd = %q, want %q", normalizedCWD, requestCWD)
	}
	classification := decodeClassification(t, record.ClassificationJSON)
	if classification.ResolvedSystem() != hook.SystemCursor {
		t.Fatalf(
			"resolved system = %q, want cursor",
			classification.ResolvedSystem(),
		)
	}
}

func TestEvaluateHookPreservesCopilotWireInput(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	wireInput := []byte(`{"sessionId":"copilot-wire","toolName":"run_in_terminal","toolUseId":"tool-1","toolInput":{"command":"true"}}`)
	_, err = srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: wireInput,
		Cwd:     t.TempDir(),
		Argv:    []string{"agent-gate", "managed-hook", "copilot", "preToolUse"},
		InvocationContext: invocationContextToProto(hook.InvocationContext{
			ManagedRegistration: hook.ObservedValue{
				Value: "copilot", Source: "managed_registration", Provenance: "hook_tag",
				Status: hook.SignalStatusObserved,
			},
		}),
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	record := onlyIntakeRecord(t, srv)
	if !bytes.Equal(record.RawPayload, wireInput) {
		t.Fatalf("raw payload changed: got %q want %q", record.RawPayload, wireInput)
	}
	var normalized map[string]json.RawMessage
	if err := json.Unmarshal(record.NormalizedJSON, &normalized); err != nil {
		t.Fatalf("decode normalized payload: %v", err)
	}
	if _, ok := normalized["session_id"]; !ok {
		t.Fatalf("normalized payload missing session_id: %s", record.NormalizedJSON)
	}
	if _, ok := normalized["hook_event_name"]; !ok {
		t.Fatalf("normalized payload missing hook_event_name: %s", record.NormalizedJSON)
	}
}

func TestEvaluateHookPersistsGenuineEmptyInput(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	response, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: []byte{},
		Argv:    []string{"agent-gate"},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 2 {
		t.Fatalf("exit code = %d, want invalid hook exit 2", response.ExitCode)
	}

	record := onlyIntakeRecord(t, srv)
	if record.ReceiptID == 0 {
		t.Fatal("receipt id = 0, want durable receipt")
	}
	if len(record.RawPayload) != 0 {
		t.Fatalf("raw payload length = %d, want 0", len(record.RawPayload))
	}
	classification := decodeClassification(t, record.ClassificationJSON)
	if classification.Result != hook.ClassificationResultInvalid {
		t.Fatalf(
			"classification result = %q, want invalid",
			classification.Result,
		)
	}

	sqliteStore := daemonSQLiteStore(t, srv)
	var payloadType string
	err = sqliteStore.Handle().QueryRowContext(
		context.Background(),
		`select typeof(content) from intake_event_details
		where event_id = ? and detail_class = 'wire_input'`,
		record.EventID,
	).Scan(&payloadType)
	if err != nil {
		t.Fatalf("query raw payload type: %v", err)
	}
	if payloadType != "blob" {
		t.Fatalf("raw payload type = %q, want blob", payloadType)
	}
}

func TestEvaluateHookClassifiesInheritedMarkers(t *testing.T) {
	setDaemonTestDirs(t)
	srv, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	wireInput := []byte(`{"hook_event_name":"preToolUse","conversation_id":"cursor-inherited","cursor_version":"1.0","tool_name":"Shell","tool_input":{"command":"true"}}`)
	_, err = srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson: wireInput,
		Argv:    []string{"agent-gate"},
		EnvFingerprint: map[string]string{
			"CLAUDE_CODE_ENTRYPOINT": "cli",
			"CODEX_THREAD_ID":        "inherited",
		},
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	record := onlyIntakeRecord(t, srv)
	if record.System != hook.SystemCursor.String() {
		t.Fatalf("record system = %q, want cursor", record.System)
	}
	classification := decodeClassification(t, record.ClassificationJSON)
	assertClassificationConflict(t, classification, hook.SystemClaude.String())
	assertClassificationConflict(t, classification, hook.SystemCodex.String())
}

func TestEvaluateHookPersistsCompleteClassificationEvidence(t *testing.T) {
	tests := []struct {
		name         string
		rawJSON      []byte
		invocation   func(*testing.T) hook.InvocationContext
		wantProvider hook.System
		wantResult   hook.ClassificationResult
		verify       func(*testing.T, hook.Classification)
	}{
		{
			name:    "missing evidence",
			rawJSON: []byte(`{"field":"value"}`),
			invocation: func(*testing.T) hook.InvocationContext {
				return hook.InvocationContext{
					WorkingDirectory: hook.ObservedValue{
						Source: "working_directory", Provenance: "operating_system",
						Status: hook.SignalStatusMissing,
					},
					CollectionIssues: []hook.CollectionIssue{{
						Source: "working_directory", Status: hook.SignalStatusMissing,
						Detail: "working directory was unavailable",
					}},
				}
			},
			wantProvider: hook.SystemUnknown,
			wantResult:   hook.ClassificationResultUnknown,
			verify: func(t *testing.T, classification hook.Classification) {
				t.Helper()
				if len(classification.Input.Invocation.CollectionIssues) != 1 {
					t.Fatalf("collection issues = %#v", classification.Input.Invocation.CollectionIssues)
				}
			},
		},
		{
			name:    "mixed inherited and conflicting evidence",
			rawJSON: []byte(`{"hook_event_name":"preToolUse","conversation_id":"c1","cursor_version":"1.0"}`),
			invocation: func(t *testing.T) hook.InvocationContext {
				return hook.InvocationContext{
					ManagedRegistration: hook.ObservedValue{
						Value: "claude", Source: "managed_registration", Provenance: "hook_tag",
						Status: hook.SignalStatusObserved,
					},
					ParentProcess: hook.ProcessEvidence{
						Name: "codex", ExecutablePath: t.TempDir(), Source: "parent_process",
						Provenance: "operating_system", Status: hook.SignalStatusObserved,
					},
					Environment: []hook.EnvironmentEvidence{{
						Name: "CLAUDE_CODE_ENTRYPOINT", Value: "cli",
						Category: "provider_environment", Source: "environment",
						Provenance: "inherited_environment", Status: hook.SignalStatusObserved,
					}},
				}
			},
			wantProvider: hook.SystemCursor,
			wantResult:   hook.ClassificationResultResolved,
			verify: func(t *testing.T, classification hook.Classification) {
				t.Helper()
				assertClassificationConflict(t, classification, hook.SystemClaude.String())
				assertClassificationConflict(t, classification, hook.SystemCodex.String())
				if len(classification.Conflicts) < 2 {
					t.Fatalf("stored conflicts = %#v", classification.Conflicts)
				}
			},
		},
		{
			name:    "hook injected evidence",
			rawJSON: []byte(`{"session_id":"s1"}`),
			invocation: func(*testing.T) hook.InvocationContext {
				return hook.InvocationContext{
					Environment: []hook.EnvironmentEvidence{{
						Name: "AGENT_GATE_HOOK_PROVIDER", Value: "copilot",
						Category: "hook_environment", Source: "environment",
						Provenance: "hook_injected", Status: hook.SignalStatusObserved,
					}},
				}
			},
			wantProvider: hook.SystemCopilot,
			wantResult:   hook.ClassificationResultResolved,
			verify: func(t *testing.T, classification hook.Classification) {
				t.Helper()
				for _, evidence := range classification.Evidence {
					if evidence.Signal == "AGENT_GATE_HOOK_PROVIDER" &&
						evidence.Provenance == "hook_injected" && evidence.Result == "match" {
						return
					}
				}
				t.Fatalf("stored injected evidence does not explain result: %#v", classification.Evidence)
			},
		},
		{
			name:         "ambiguous evidence",
			rawJSON:      []byte(`{"hook_event_name":"preToolUse"}`),
			invocation:   func(*testing.T) hook.InvocationContext { return hook.InvocationContext{} },
			wantProvider: hook.SystemUnknown,
			wantResult:   hook.ClassificationResultAmbiguous,
			verify: func(t *testing.T, classification hook.Classification) {
				t.Helper()
				if len(classification.Evidence) != 2 {
					t.Fatalf("ambiguous evidence = %#v, want two candidates", classification.Evidence)
				}
			},
		},
		{
			name:         "unknown evidence",
			rawJSON:      []byte(`{"unrecognizedField":"value"}`),
			invocation:   func(*testing.T) hook.InvocationContext { return hook.InvocationContext{} },
			wantProvider: hook.SystemUnknown,
			wantResult:   hook.ClassificationResultUnknown,
			verify: func(t *testing.T, classification hook.Classification) {
				t.Helper()
				fields := classification.Input.Payload.Fields
				if len(fields) != 1 || fields[0].Name != "unrecognizedField" {
					t.Fatalf("stored payload fields = %#v", fields)
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			setDaemonTestDirs(t)
			srv, err := New(newDiscardLogger(), daemonTestConfig(t))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer srv.Close()

			_, err = srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
				RawJson:           testCase.rawJSON,
				Argv:              []string{"agent-gate", "managed-hook", "claude"},
				InvocationContext: invocationContextToProto(testCase.invocation(t)),
			})
			if err != nil {
				t.Fatalf("EvaluateHook: %v", err)
			}
			classification := decodeClassification(t, onlyIntakeRecord(t, srv).ClassificationJSON)
			if classification.ResolvedSystem() != testCase.wantProvider {
				t.Fatalf("resolved provider = %q, want %q", classification.ResolvedSystem(), testCase.wantProvider)
			}
			if classification.Result != testCase.wantResult {
				t.Fatalf("result = %q, want %q", classification.Result, testCase.wantResult)
			}
			testCase.verify(t, classification)
		})
	}
}

func daemonSQLiteStore(t *testing.T, srv *Server) *intake.Store {
	t.Helper()
	snapshot := srv.runtime.Load()
	if snapshot == nil {
		t.Fatal("runtime snapshot is nil")
	}
	store, ok := snapshot.intakeStore.(*sqliteIntakeStore)
	if !ok || store.store == nil {
		t.Fatalf("intake store = %T, want sqlite store", snapshot.intakeStore)
	}
	return store.store
}

func onlyIntakeRecord(t *testing.T, srv *Server) intake.Record {
	t.Helper()
	store := daemonSQLiteStore(t, srv)
	var eventID string
	var count int
	err := store.Handle().QueryRowContext(
		context.Background(),
		`select min(event_id), count(*) from intake_events`,
	).Scan(&eventID, &count)
	if err != nil {
		t.Fatalf("query intake event: %v", err)
	}
	if count != 1 {
		t.Fatalf("intake event count = %d, want 1", count)
	}
	record, err := store.Get(context.Background(), eventID)
	if err != nil {
		t.Fatalf("Get intake event: %v", err)
	}
	return record
}

func decodeClassification(
	t *testing.T,
	rawClassification json.RawMessage,
) hook.Classification {
	t.Helper()
	var classification hook.Classification
	if err := json.Unmarshal(rawClassification, &classification); err != nil {
		t.Fatalf("decode classification: %v", err)
	}
	return classification
}

func assertClassificationConflict(
	t *testing.T,
	classification hook.Classification,
	provider string,
) {
	t.Helper()
	for _, evidence := range classification.Evidence {
		if evidence.Provider == provider && evidence.Result == "conflict" {
			return
		}
	}
	t.Fatalf("classification missing %s conflict: %#v", provider, classification.Evidence)
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

func TestResolveHookEnvironment_DaemonOwnsPayloadParsing(t *testing.T) {
	server := &Server{}
	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"cat \"` + "$" + `TARGET\" ` + "$" + `SECOND"}}`)

	response, err := server.ResolveHookEnvironment(
		context.Background(),
		&daemonpb.ResolveHookEnvironmentRequest{
			RawJson: rawJSON, ProviderHint: "codex",
		},
	)
	if err != nil {
		t.Fatalf("ResolveHookEnvironment: %v", err)
	}
	want := []string{"TARGET"}
	if fmt.Sprint(response.ReferencedNames) != fmt.Sprint(want) {
		t.Fatalf("referenced names = %v, want %v", response.ReferencedNames, want)
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
	// An overloaded queue means the call was let through without being
	// evaluated, so it says so rather than looking like a clean allow.
	assertSaysUnevaluated(t, resp, hook.FailOpenReasonOverloaded)
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

func TestServerCloseWaitsForAdmittedEvaluation(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	evaluationEntered := make(chan struct{})
	releaseEvaluation := make(chan struct{})
	setHotEvaluatorForTest(
		t,
		server,
		func(
			ctx context.Context,
			input hook.EvaluationInput,
			cfg *config.Config,
			getenv func(string) string,
			eventID string,
		) hook.HotEvaluation {
			close(evaluationEntered)
			<-releaseEvaluation
			return defaultHotEvaluate(ctx, input, cfg, getenv, eventID)
		},
	)

	evaluationDone := make(chan struct{})
	go func() {
		defer close(evaluationDone)
		_, _ = server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
			RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
			ProviderHint: "codex",
		})
	}()
	<-evaluationEntered

	closeDone := make(chan struct{})
	go func() {
		server.Close()
		close(closeDone)
	}()

	closedBeforeRelease := false
	select {
	case <-closeDone:
		closedBeforeRelease = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseEvaluation)
	<-evaluationDone
	<-closeDone

	if closedBeforeRelease {
		t.Fatal("Server.Close returned while an admitted evaluation still used its snapshot")
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

func TestPublicKVRPCsRejectTemporalInternalNamespace(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := temporalDaemonConfig(t, `
[[rules]]
name = "temporal-validator"
cursor_events = ["stop"]
action = "inject"
output = "configured fallback"

[[rules.conditions]]
kind = "exec"
command = ["/bin/validator"]
field_paths = ["last_user_message", "last_response_output"]
cache_ttl_ms = 0
`)
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	runner := &daemonInputRunner{mu: sync.Mutex{}}
	runtime := rules.NewExecRuntimeWithCache(runner, newDiscardLogger(), server.hotKV)
	fields := rules.FieldSet{ConversationID: "conversation-1"}
	if !runtime.ObserveUserPrompt("cursor", fields, 10, "private prompt") {
		t.Fatal("ObserveUserPrompt did not store the prompt")
	}
	if !runtime.ObserveResponseOutput(
		"cursor",
		fields,
		"stop",
		config.ActionInject,
		"context",
		9,
		"private response",
	) {
		t.Fatal("ObserveResponseOutput did not store the response")
	}

	namespace := hotkv.InternalNamespacePrefix + "exec-temporal"
	originalEntries, err := server.hotKV.List(namespace, "", 0, true)
	if err != nil {
		t.Fatalf("List internal temporal entries: %v", err)
	}
	if len(originalEntries) != 2 {
		t.Fatalf("internal temporal entries = %d, want 2", len(originalEntries))
	}
	key := originalEntries[0].Key
	keyPrefix := key[:8]
	restoreEntries := func() {
		t.Helper()
		for _, entry := range originalEntries {
			_, _, restoreErr := server.hotKV.Set(
				namespace,
				entry.Key,
				entry.Value,
				hotkv.SetOptions{},
			)
			if restoreErr != nil {
				t.Fatalf("restore internal temporal entry: %v", restoreErr)
			}
		}
	}

	ctx := context.Background()
	operations := []struct {
		name string
		call func() error
	}{
		{
			name: "get",
			call: func() error {
				_, callErr := server.KVGet(
					ctx,
					&daemonpb.KVGetRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "set",
			call: func() error {
				_, callErr := server.KVSet(ctx, &daemonpb.KVSetRequest{
					Namespace: namespace,
					Key:       key,
					Value:     []byte("corrupt"),
				})
				return callErr
			},
		},
		{
			name: "delete",
			call: func() error {
				_, callErr := server.KVDelete(
					ctx,
					&daemonpb.KVDeleteRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "exists",
			call: func() error {
				_, callErr := server.KVExists(
					ctx,
					&daemonpb.KVExistsRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "ttl",
			call: func() error {
				_, callErr := server.KVTTL(
					ctx,
					&daemonpb.KVGetRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "pttl",
			call: func() error {
				_, callErr := server.KVPTTL(
					ctx,
					&daemonpb.KVGetRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "expire",
			call: func() error {
				_, callErr := server.KVExpire(ctx, &daemonpb.KVExpireRequest{
					Namespace: namespace,
					Key:       key,
					TtlMs:     1000,
				})
				return callErr
			},
		},
		{
			name: "get-delete",
			call: func() error {
				_, callErr := server.KVGetDelete(
					ctx,
					&daemonpb.KVGetDeleteRequest{Namespace: namespace, Key: key},
				)
				return callErr
			},
		},
		{
			name: "list-all-keys",
			call: func() error {
				_, callErr := server.KVList(ctx, &daemonpb.KVListRequest{
					Namespace:     namespace,
					IncludeValues: true,
				})
				return callErr
			},
		},
		{
			name: "list-key-prefix",
			call: func() error {
				_, callErr := server.KVList(ctx, &daemonpb.KVListRequest{
					Namespace:     namespace,
					Prefix:        keyPrefix,
					IncludeValues: true,
				})
				return callErr
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			restoreEntries()
			callErr := operation.call()
			if status.Code(callErr) != codes.PermissionDenied {
				t.Fatalf(
					"status = %v, want %v",
					status.Code(callErr),
					codes.PermissionDenied,
				)
			}
			const wantMessage = "internal namespace is not available through public KV RPCs"
			if status.Convert(callErr).Message() != wantMessage {
				t.Fatalf(
					"message = %q, want %q",
					status.Convert(callErr).Message(),
					wantMessage,
				)
			}
		})
	}

	restoreEntries()
	evaluationContext := rules.WithExecRuntime(context.Background(), runtime)
	evaluationContext = rules.WithExecResponseTargetResolver(
		evaluationContext,
		func(string) string { return "context" },
	)
	rules.EvaluateAllDetailed(
		evaluationContext,
		"cursor",
		"stop",
		fields,
		cfg.Rules,
		nil,
		nil,
		"",
	)
	input := runner.Input()
	assertDaemonMatchedField(t, input, "last_user_message", "private prompt", true)
	assertDaemonMatchedField(t, input, "last_response_output", "private response", true)
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
	setHotEvaluatorForTest(t, srv, func(ctx context.Context, input hook.EvaluationInput, cfg *config.Config, getenv func(string) string, eventID string) hook.HotEvaluation {
		hotCalled = true
		return hook.EvaluateClassifiedHotWithEventID(ctx, input, cfg, getenv, eventID)
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
	assertSaysUnevaluated(t, resp, hook.FailOpenReasonIntakeWriteFailed)
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
	snapshot := srv.runtime.Load()
	originalHotEvaluate := snapshot.hotEvaluate
	hotEvaluationCalled := false
	snapshot.hotEvaluate = func(
		ctx context.Context,
		input hook.EvaluationInput,
		syncConfig *config.Config,
		getenv func(string) string,
		eventID string,
	) hook.HotEvaluation {
		hotEvaluationCalled = true
		return originalHotEvaluate(ctx, input, syncConfig, getenv, eventID)
	}

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
	if hotEvaluationCalled {
		t.Fatal("observe-only Stop ran synchronously")
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

func TestEvaluateHook_ClaudeSessionEndReturnsAfterDurableIntake(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := codexStopAuditDaemonTestConfig(t)
	srv, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()
	snapshot := srv.runtime.Load()
	snapshot.hotEvaluate = func(
		context.Context,
		hook.EvaluationInput,
		*config.Config,
		func(string) string,
		string,
	) hook.HotEvaluation {
		t.Fatal("observe-only SessionEnd ran synchronously")
		return hook.HotEvaluation{}
	}

	rawJSON := []byte(`{"session_id":"s1","hook_event_name":"SessionEnd","cwd":"/repo","reason":"exit"}`)
	resp, err := srv.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:      rawJSON,
		ProviderHint: "claude",
		Cwd:          "/repo",
	})
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if string(resp.StdoutData) != "{}\n" {
		t.Fatalf("stdout = %q, want allow response", string(resp.StdoutData))
	}
	waitForNoPendingIntake(t, srv)
	waitForAuditMessages(t, cfg, "hook.allowed")
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

func TestReloadConfigInvalidAuditStorageKeepsPreviousSnapshot(t *testing.T) {
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
	previousSnapshot := srv.runtime.Load()

	writeConfig(t, configPath, `
[audit]
enabled = false

[audit.storage]
max_size_mb = -1

[[rules]]
name = "block-beta"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "beta"
action = "block"
violation_message = "beta blocked"
`)
	if err := srv.reloadConfig(context.Background()); err == nil {
		t.Fatal("reloadConfig succeeded, want audit storage error")
	}
	if currentSnapshot := srv.runtime.Load(); currentSnapshot != previousSnapshot {
		t.Fatal("reloadConfig replaced the active runtime snapshot")
	}

	assertCommandDecision(t, srv, "alpha", 0, "block-alpha")
	assertCommandDecision(t, srv, "beta", 0, "")
}

func TestReloadConfigWrongTypeAuditStorageKeepsPreviousSnapshot(t *testing.T) {
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
	previousSnapshot := srv.runtime.Load()

	writeConfig(t, configPath, `
[audit.storage]
max_size_mb = "25"
`)
	reloadErr := srv.reloadConfig(context.Background())
	if reloadErr == nil {
		t.Error("reloadConfig succeeded, want document decode error")
	}
	if currentSnapshot := srv.runtime.Load(); currentSnapshot != previousSnapshot {
		t.Error("reloadConfig replaced the active runtime snapshot")
	}

	assertCommandDecision(t, srv, "alpha", 0, "block-alpha")
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

type failingIntakeStore struct{ intakeStore }

func (failingIntakeStore) Append(context.Context, intake.Record) (intake.AppendResult, error) {
	return intake.AppendResult{}, errors.New("append failed")
}

func (failingIntakeStore) Get(context.Context, string) (intake.Record, error) {
	return intake.Record{}, errors.New("get failed")
}

func (failingIntakeStore) GetReceipt(context.Context, int64) (intake.Record, error) {
	return intake.Record{}, errors.New("get receipt failed")
}

func (failingIntakeStore) MarkDeferredPending(context.Context, string, int64) error {
	return errors.New("mark pending failed")
}

func (failingIntakeStore) MarkDeferredComplete(context.Context, int64) error {
	return errors.New("mark complete failed")
}

func (failingIntakeStore) ClaimDeferred(
	context.Context,
	int64,
	string,
	time.Duration,
) (intake.Record, intake.DeferredClaim, error) {
	return intake.Record{}, intake.DeferredClaim{}, errors.New("claim failed")
}

func (failingIntakeStore) ReleaseDeferredClaim(
	context.Context,
	intake.DeferredClaim,
) error {
	return errors.New("release claim failed")
}

func (failingIntakeStore) RenewDeferredClaim(
	context.Context,
	intake.DeferredClaim,
	time.Duration,
) error {
	return errors.New("renew claim failed")
}

func (failingIntakeStore) ReplayPending(context.Context, func(intake.Record) error) error {
	return errors.New("replay failed")
}

func (failingIntakeStore) ListPending(context.Context) ([]int64, error) {
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
		snapshot.inferRuntime,
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
	snapshot.deferredProcessor.events <- deferredWork{
		receiptID: 1,
		eventID:   "occupied",
		hotEvent:  hook.DeferredAuditEvent{},
	}
}

func setHotEvaluatorForTest(t testing.TB, srv *Server, evaluator func(context.Context, hook.EvaluationInput, *config.Config, func(string) string, string) hook.HotEvaluation) {
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
