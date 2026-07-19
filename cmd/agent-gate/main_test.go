package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

type readError struct{}

func (readError) Read(_ []byte) (int, error) {
	return 0, errors.New("read failed")
}

type fakeHookClient struct {
	response *daemonpb.EvaluateHookResponse
	err      error
}

type environmentRecordingHookClient struct {
	environment      map[string]string
	referencedNames  []string
	referenceRequest []byte
}

func (client *environmentRecordingHookClient) ResolveHookEnvironment(
	rawJSON []byte,
	_ string,
	_ []string,
	_ map[string]string,
) ([]string, error) {
	client.referenceRequest = append([]byte(nil), rawJSON...)
	return client.referencedNames, nil
}

func (client *environmentRecordingHookClient) EvaluateHook(
	_ []byte,
	_ string,
	_ string,
	_ []string,
	environment map[string]string,
) (*daemonpb.EvaluateHookResponse, error) {
	client.environment = environment
	return &daemonpb.EvaluateHookResponse{}, nil
}

func (client *environmentRecordingHookClient) Close() error {
	return nil
}

func (client fakeHookClient) EvaluateHook(_ []byte, _ string, _ string, _ []string, _ map[string]string) (*daemonpb.EvaluateHookResponse, error) {
	if client.err != nil {
		return nil, client.err
	}
	return client.response, nil
}

func (client fakeHookClient) ResolveHookEnvironment(
	_ []byte,
	_ string,
	_ []string,
	_ map[string]string,
) ([]string, error) {
	return nil, nil
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

func TestRunHookForwardsReferencedCommandEnvironment(t *testing.T) {
	client := &environmentRecordingHookClient{
		environment:     nil,
		referencedNames: []string{"TARGET", "PRIVATE"},
	}
	connect := func(context.Context) (hookClient, error) {
		return client, nil
	}
	payload := `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo x > \"` + "$" + `TARGET\""}}`
	runtime, _, _ := testHookRuntime(strings.NewReader(payload), connect)
	runtime.getenv = func(name string) string {
		values := map[string]string{
			"TARGET":  "/repo/main/file.txt",
			"PRIVATE": "do-not-forward",
		}
		return values[name]
	}

	exitCode := runHookWithRuntime(hook.SystemCodex, runtime)

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	if len(client.environment) != 1 || client.environment["TARGET"] != "/repo/main/file.txt" {
		t.Fatalf("forwarded environment = %v", client.environment)
	}
	if string(client.referenceRequest) != payload {
		t.Fatalf("reference request = %q, want raw payload", client.referenceRequest)
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

func TestStripJSONFlagOnlyConsumesLeadingFlags(t *testing.T) {
	args, jsonOut := stripJSONFlag([]string{"--json", "get", "namespace", "--json"})
	if !jsonOut {
		t.Fatal("jsonOut = false, want true")
	}
	if len(args) != 3 {
		t.Fatalf("args length = %d, want 3 (%v)", len(args), args)
	}
	if args[0] != "get" || args[1] != "namespace" || args[2] != "--json" {
		t.Fatalf("args = %v, want trailing --json preserved", args)
	}
}

func TestJSONEntryUsesBase64ForBinaryValue(t *testing.T) {
	entry := jsonEntry(&daemonpb.KVEntry{
		Namespace:       "test",
		Key:             "binary",
		Value:           []byte{0xff, 0x00, 0x61},
		Version:         1,
		CreatedUnixNano: 1,
		UpdatedUnixNano: 2,
		ExpiresUnixNano: 3,
		PttlMs:          4,
	}, true)
	if entry == nil {
		t.Fatal("jsonEntry returned nil")
	}
	if entry.Value != "" {
		t.Fatalf("entry.Value = %q, want empty binary-safe JSON output", entry.Value)
	}
	if entry.ValueBase64 != "/wBh" {
		t.Fatalf("entry.ValueBase64 = %q, want /wBh", entry.ValueBase64)
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
	logger, err := audit.NewEventLoggerWithOptions(context.Background(), &config.Config{}, nil, audit.LoggerOptions{QueueLimit: 0})
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

func TestRunQueryEvaluationsEmitsSafeNestedJSONLWithFilters(t *testing.T) {
	setupQueryEnvironment(t)
	record := appendCLIQueryEvaluation(t)

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"evaluations",
		"--evaluation-id", record.Evaluation.EvaluationID,
		"--event-id", record.Evaluation.EventID,
		"--receipt-id", strconv.FormatInt(record.Evaluation.ReceiptID, 10),
		"--mode", record.Evaluation.Mode,
		"--since", "2026-07-11T00:00:00Z",
		"--until", "2026-07-12T00:00:00Z",
		"--system", "codex",
		"--session", "session-cli",
		"--event", "PreToolUse",
		"--tool", "exec_command",
		"--rule", "cli-rule",
		"--layer", "cli-layer",
		"--kind", "inference",
		"--outcome", "match",
		"--model", "gpt-cli",
		"--verdict", "block",
		"--limit", "10",
		"--offset", "0",
		"--json",
	})

	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("JSONL lines = %d, want 1: %q", len(lines), stdout)
	}
	var got evaluation.QueryRecord
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("decode JSONL: %v", err)
	}
	if got.EvaluationID != record.Evaluation.EvaluationID || len(got.Layers) != 1 ||
		got.Layers[0].Outcome != "match" || got.Layers[0].ModelName != "gpt-cli" {
		t.Fatalf("JSONL record = %+v", got)
	}
	for _, prohibited := range []string{"selected input secret", "backend secret", "authorization", "rationale"} {
		if strings.Contains(strings.ToLower(stdout), prohibited) {
			t.Fatalf("JSONL exposes prohibited %q: %s", prohibited, stdout)
		}
	}
	if !strings.Contains(stdout, `"verified_provenance":{"requested_model":"gpt-cli","reported_prompt_hash_status":"absent","reported_schema_hash_status":"absent"}`) ||
		!strings.Contains(stdout, `"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"present","raw":{"prompt_tokens":"0"}}`) ||
		strings.Contains(stdout, "completion_tokens") {
		t.Fatalf("JSONL provenance envelope = %s", stdout)
	}
}

func TestRunQueryEvaluationsPrintsSafeSummaryTable(t *testing.T) {
	setupQueryEnvironment(t)
	record := appendCLIQueryEvaluation(t)

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"evaluations", "--evaluation-id", record.Evaluation.EvaluationID,
	})

	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	for _, required := range []string{
		"source=sqlite rows=1", "completed_at", "codex", "hot", "block",
		"PreToolUse", "exec_command", record.Evaluation.EvaluationID,
	} {
		if !strings.Contains(stdout, required) {
			t.Fatalf("table missing %q: %s", required, stdout)
		}
	}
	for _, prohibited := range []string{"selected input secret", "backend secret", "authorization"} {
		if strings.Contains(strings.ToLower(stdout), prohibited) {
			t.Fatalf("table exposes prohibited %q: %s", prohibited, stdout)
		}
	}
}

func TestRunQueryEvaluationsHandlesEmptyHistory(t *testing.T) {
	setupQueryEnvironment(t)

	exitCode, stdout, stderr := captureRunQuery(t, []string{"evaluations", "--json"})

	if exitCode != 0 || stdout != "" {
		t.Fatalf("exitCode = %d, stdout = %q, stderr = %q", exitCode, stdout, stderr)
	}
	if !strings.Contains(stderr, "no evaluation history") {
		t.Fatalf("stderr = %q, want friendly empty history note", stderr)
	}
}

func TestExistingQueryTableRenderersRemainByteCompatible(t *testing.T) {
	seen := intake.QueryResult{
		Source: "sqlite",
		Records: []intake.QueryRecord{
			{
				RecordedAt: "2026-07-11T01:02:03Z", System: "codex",
				SessionID: "session-1", EventName: "PreToolUse", ToolName: "Shell",
				Operation: intake.Operation{Command: "make test"},
				Deferred:  intake.QueryDeferred{State: intake.DeferredStatePending},
			},
		},
	}
	seenOutput := captureStdoutCall(t, func() { printSeenTable(seen) })
	wantSeen := "source=sqlite rows=1\n" +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %s\n", "recorded_at", "system", "state", "event", "tool", "session", "command") +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %s\n", "2026-07-11T01:02:03Z", "codex", "pending", "PreToolUse", "Shell", "session-1", "make test")
	if seenOutput != wantSeen {
		t.Fatalf("seen table changed\ngot:  %q\nwant: %q", seenOutput, wantSeen)
	}

	events := []audit.Event{
		{
			Time: "2026-07-11T01:02:03Z", System: "codex", EventName: "PreToolUse",
			ToolName: "Shell", Operation: audit.Operation{Command: "make test"},
			Decision: audit.Decision{Kind: "block", RulesMatched: []string{"rule-1"}},
		},
	}
	eventOutput := captureStdoutCall(t, func() { printEventTable("sqlite", events) })
	wantEvent := "source=sqlite rows=1\n" +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n", "time", "system", "decision", "event", "tool", "rules", "command") +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n", "2026-07-11T01:02:03Z", "codex", "block", "PreToolUse", "Shell", "rule-1", "make test")
	if eventOutput != wantEvent {
		t.Fatalf("decision table changed\ngot:  %q\nwant: %q", eventOutput, wantEvent)
	}
}

func appendCLIQueryEvaluation(t *testing.T) evaluation.Record {
	t.Helper()
	ctx := context.Background()
	store, err := intake.OpenSQLite(ctx, config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	receipt, err := store.Append(ctx, intake.Record{
		EventID: "evt-cli-evaluation", RecordedAt: time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC),
		System: "codex", SessionID: "session-cli", EventName: "PreToolUse",
		ToolName: "exec_command", RawPayload: []byte(`{"authorization":"raw"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	startedAt := time.Date(2026, 7, 11, 1, 0, 1, 0, time.UTC)
	record := evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: "eval-cli", ReceiptID: receipt.ReceiptID, EventID: receipt.EventID,
			Attempt: 1, Mode: "hot", ConfigHash: "sha256:config", EngineVersion: "v1",
			EngineCommit: "commit", EngineBuildHash: "sha256:build", InputHash: "sha256:input",
			StartedAt: startedAt, CompletedAt: startedAt.Add(time.Millisecond),
			FinalVerdict: "block", FinalSource: "inference", EnforcementAction: "deny",
			Enforced: true, TotalLatencyUS: 1000, ErrorJSON: json.RawMessage(`{}`),
		},
		Layers: []evaluation.Layer{
			{
				LayerIndex: 0, Kind: "inference", Name: "cli-layer", Status: "complete",
				Outcome: "match", InputReference: "intake.normalized_json",
				InputJSON:  json.RawMessage(`{"input":"selected input secret","authorization":"backend secret"}`),
				InputHash:  "sha256:layer-input",
				OutputHash: cliQueryOutputHash(json.RawMessage(`{"decision":"block"}`)),
				OutputJSON: json.RawMessage(`{"decision":"block"}`),
				MetadataJSON: json.RawMessage(`{
					"schema_version":2,
					"rule_name":"cli-rule",
					"verified_provenance":{
						"requested_model":"gpt-cli",
						"reported_prompt_hash_status":"absent",
						"reported_schema_hash_status":"absent"
					},
					"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"present","raw":{"prompt_tokens":"0"}}
				}`),
				StartedAt: startedAt, CompletedAt: startedAt.Add(time.Millisecond), LatencyUS: 1000,
				ServiceName: "inference", ModelName: "gpt-cli", PromptHash: "sha256:prompt",
				SchemaHash: "sha256:schema", ErrorMessage: "backend secret",
			},
		},
		Labels: []evaluation.Label{
			{
				Namespace: "human", LabelVersion: 1, Verdict: "block", Source: "reviewer",
				Rationale: "authorization", CreatedAt: startedAt.Add(time.Second),
			},
		},
	}
	if err := store.Evaluations().RecordCompleted(ctx, record); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	return record
}

func cliQueryOutputHash(value json.RawMessage) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func captureStdoutCall(t *testing.T, call func()) string {
	t.Helper()
	stdoutFile, err := os.CreateTemp(t.TempDir(), "stdout-*")
	if err != nil {
		t.Fatalf("CreateTemp stdout: %v", err)
	}
	originalStdout := os.Stdout
	os.Stdout = stdoutFile
	defer func() {
		os.Stdout = originalStdout
	}()
	call()
	return readCapturedFile(t, stdoutFile)
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
		getenv: func(string) string { return "" },
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
