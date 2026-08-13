package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
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
	invocation       hook.InvocationContext
}

func (client *environmentRecordingHookClient) ResolveHookEnvironment(
	rawJSON []byte,
	_ string,
	_ []string,
	_ map[string]string,
	invocation hook.InvocationContext,
) ([]string, error) {
	client.referenceRequest = append([]byte(nil), rawJSON...)
	client.invocation = invocation
	return client.referencedNames, nil
}

func (client *environmentRecordingHookClient) EvaluateHook(
	_ []byte,
	_ string,
	_ string,
	_ []string,
	environment map[string]string,
	invocation hook.InvocationContext,
) (*daemonpb.EvaluateHookResponse, error) {
	client.environment = environment
	client.invocation = invocation
	return &daemonpb.EvaluateHookResponse{}, nil
}

func (client *environmentRecordingHookClient) Close() error {
	return nil
}

func (client fakeHookClient) EvaluateHook(
	_ []byte,
	_ string,
	_ string,
	_ []string,
	_ map[string]string,
	_ hook.InvocationContext,
) (*daemonpb.EvaluateHookResponse, error) {
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
	_ hook.InvocationContext,
) ([]string, error) {
	return nil, nil
}

func (client fakeHookClient) Close() error {
	return nil
}

func TestRunCLIHelpDoesNotEnterHookMode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	hookCalls := 0

	exitCode := runCLIWithHook(
		[]string{"--help"},
		&stdout,
		&stderr,
		func(hookRoute) int {
			hookCalls++
			return 0
		},
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if hookCalls != 0 {
		t.Fatalf("hook calls = %d, want 0", hookCalls)
	}
	if !strings.Contains(stdout.String(), "Usage: agent-gate") {
		t.Fatalf("stdout = %q, want usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunCLIHelpListsSetupAndSetupFlags(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := runCLIWithHook([]string{"--help"}, &stdout, &stderr, func(hookRoute) int {
		t.Fatal("hook called")
		return 1
	}); exitCode != 0 {
		t.Fatalf("help exit code = %d", exitCode)
	}
	if !strings.Contains(stdout.String(), "setup") {
		t.Fatalf("help = %q", stdout.String())
	}
	stdout.Reset()
	if exitCode := runCLIWithHook([]string{"setup", "--help"}, &stdout, &stderr, func(hookRoute) int {
		t.Fatal("hook called")
		return 1
	}); exitCode != 0 {
		t.Fatalf("setup help exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for _, flagName := range []string{"--non-interactive", "--providers", "--audit-profile", "--auto-update", "--bin-path", "--json"} {
		if !strings.Contains(stdout.String(), flagName) {
			t.Fatalf("setup help = %q, want %s", stdout.String(), flagName)
		}
	}
}

func TestRunCLIHelpListsAuditCommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runCLIWithHook(
		[]string{"--help"},
		&stdout,
		&stderr,
		func(hookRoute) int { return 0 },
	)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "audit          Inspect and maintain audit storage") {
		t.Fatalf("stdout = %q, want audit command", stdout.String())
	}
}

func TestRunCLIUnknownCommandDoesNotEnterHookMode(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	hookCalls := 0

	exitCode := runCLIWithHook(
		[]string{"not-a-command"},
		&stdout,
		&stderr,
		func(hookRoute) int {
			hookCalls++
			return 0
		},
	)

	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if hookCalls != 0 {
		t.Fatalf("hook calls = %d, want 0", hookCalls)
	}
	if !strings.Contains(stderr.String(), `unknown command "not-a-command"`) {
		t.Fatalf("stderr = %q, want unknown command", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestAgentGateIgnoresSnapshotHelperEnvironmentWithoutDescriptors(t *testing.T) {
	binaryPath := filepath.Join(t.TempDir(), "agent-gate")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v\n%s", err, output)
	}
	command := exec.CommandContext(t.Context(), binaryPath, "help")
	command.Env = append(os.Environ(), "AGENT_GATE_SNAPSHOT_LOCK_HELPER=1")

	output, err := command.CombinedOutput()

	if err != nil {
		t.Fatalf("agent-gate help with helper marker: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("Usage: agent-gate")) {
		t.Fatalf("output = %q, want normal help", output)
	}
}

func TestRunCLINoArgumentsPreservesBareHookMode(t *testing.T) {
	hookCalls := 0
	exitCode := runCLIWithHook(
		nil,
		io.Discard,
		io.Discard,
		func(route hookRoute) int {
			hookCalls++
			if route.ProviderHint != hook.SystemUnknown {
				t.Fatalf("hook system = %q, want unknown", route.ProviderHint)
			}
			return 7
		},
	)

	if exitCode != 7 {
		t.Fatalf("exit code = %d, want hook exit 7", exitCode)
	}
	if hookCalls != 1 {
		t.Fatalf("hook calls = %d, want 1", hookCalls)
	}
}

func TestRunCLIManagedHookRecordsRegistrationWithoutExplicitHint(t *testing.T) {
	var received hookRoute
	exitCode := runCLIWithHook(
		[]string{"managed-hook", "claude"},
		io.Discard,
		io.Discard,
		func(route hookRoute) int {
			received = route
			return 0
		},
	)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if received.ProviderHint != hook.SystemUnknown {
		t.Fatalf("provider hint = %q, want unknown", received.ProviderHint)
	}
	if received.ManagedRegistration != "claude" {
		t.Fatalf("managed registration = %q, want claude", received.ManagedRegistration)
	}
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
	// stdout above is unchanged, so the provider still reads an allow. stderr
	// now says the call went unevaluated, because a fail-open that looks exactly
	// like a clean allow is what let an outage pass as compliance.
	if !strings.Contains(stderr.String(), "no rule was enforced") {
		t.Fatalf("stderr = %q, want the fail-open notice", stderr.String())
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
	// stdout above is unchanged, so the provider still reads an allow. stderr
	// now says the call went unevaluated, because a fail-open that looks exactly
	// like a clean allow is what let an outage pass as compliance.
	if !strings.Contains(stderr.String(), "no rule was enforced") {
		t.Fatalf("stderr = %q, want the fail-open notice", stderr.String())
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
	// stdout above is unchanged, so the provider still reads an allow. stderr
	// now says the call went unevaluated, because a fail-open that looks exactly
	// like a clean allow is what let an outage pass as compliance.
	if !strings.Contains(stderr.String(), "no rule was enforced") {
		t.Fatalf("stderr = %q, want the fail-open notice", stderr.String())
	}
}

func TestRunHookFailOpenOnHookPanic(t *testing.T) {
	connect := func(context.Context) (hookClient, error) {
		panic("test panic")
	}
	runtime, stdout, stderr := testHookRuntime(strings.NewReader(`{"hook_event_name":"PreToolUse"}`), connect)

	exitCode := runHookWithRuntime(hook.SystemClaude, runtime)

	// A recovered panic must never block the call, and it must not be silent
	// either: the agent is told the call went unevaluated. Asserting a bare
	// Claude allow here was asserting that an outage looks like compliance.
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
	got := stdout.String()
	if !strings.Contains(got, "no rule was enforced") {
		t.Fatalf("stdout = %q, want the fail-open notice", got)
	}
	if !strings.Contains(got, string(hook.FailOpenReasonPanic)) {
		t.Fatalf("stdout = %q, want it to name the panic reason", got)
	}
	if !strings.Contains(stderr.String(), "no rule was enforced") {
		t.Fatalf("stderr = %q, want the fail-open notice", stderr.String())
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
	if client.invocation.WorkingDirectory.Value != "test-working-directory" {
		t.Fatalf("invocation working directory = %#v", client.invocation.WorkingDirectory)
	}
}

func TestCollectHookInvocationContextPreservesSignalProvenance(t *testing.T) {
	workingDirectory := t.TempDir()
	executablePath := filepath.Join(t.TempDir(), "agent-gate")
	parentPath := filepath.Join(t.TempDir(), "shell")
	ancestorPath := filepath.Join(t.TempDir(), "harness")
	context := collectHookInvocationContext(
		[]string{executablePath, "managed-hook", "claude"},
		"claude",
		func() (string, error) { return workingDirectory, nil },
		func() (string, error) { return executablePath, nil },
		map[string]string{
			"CLAUDE_CODE_ENTRYPOINT":   "cli",
			"CODEX_THREAD_ID":          "inherited-thread",
			"AGENT_GATE_HOOK_PROVIDER": "claude",
		},
		func() (hook.ProcessEvidence, []hook.ProcessEvidence, error) {
			return hook.ProcessEvidence{
					Name: "shell", ExecutablePath: parentPath,
					Source: "parent_process", Provenance: "operating_system",
					Status: hook.SignalStatusObserved,
				}, []hook.ProcessEvidence{{
					Name: "harness", ExecutablePath: ancestorPath,
					Source: "ancestor_process", Provenance: "operating_system",
					Status: hook.SignalStatusObserved,
				}}, nil
		},
	)

	if context.HookSubcommand.Value != "managed-hook" {
		t.Fatalf("hook subcommand = %q, want managed-hook", context.HookSubcommand.Value)
	}
	if len(context.HookTags) != 1 || context.HookTags[0].Value != "claude" {
		t.Fatalf("hook tags = %#v, want claude", context.HookTags)
	}
	if context.ManagedRegistration.Value != "claude" {
		t.Fatalf("managed registration = %#v, want claude", context.ManagedRegistration)
	}
	if context.WorkingDirectory.Value != workingDirectory {
		t.Fatalf("working directory = %#v", context.WorkingDirectory)
	}
	if context.Executable.ExecutablePath != executablePath {
		t.Fatalf("executable = %#v", context.Executable)
	}
	if context.ParentProcess.Name != "shell" || len(context.Ancestors) != 1 {
		t.Fatalf("process evidence = parent %#v ancestors %#v", context.ParentProcess, context.Ancestors)
	}
	if len(context.Environment) != 3 {
		t.Fatalf("environment evidence = %#v", context.Environment)
	}
	for _, signal := range context.Environment {
		if signal.Provenance != "inherited_environment" {
			t.Fatalf("environment provenance = %q", signal.Provenance)
		}
		if signal.Category != "provider_environment" {
			t.Fatalf("environment category = %q", signal.Category)
		}
	}
}

func TestCollectHookInvocationContextRecordsUnavailableEvidence(t *testing.T) {
	context := collectHookInvocationContext(
		[]string{"agent-gate"},
		"",
		func() (string, error) { return "", errors.New("cwd unavailable") },
		func() (string, error) { return "", errors.New("executable unavailable") },
		nil,
		func() (hook.ProcessEvidence, []hook.ProcessEvidence, error) {
			return hook.ProcessEvidence{}, nil, errors.New("process table unavailable")
		},
	)

	if context.WorkingDirectory.Status != hook.SignalStatusUnreadable {
		t.Fatalf("working directory = %#v", context.WorkingDirectory)
	}
	if context.Executable.Status != hook.SignalStatusUnreadable {
		t.Fatalf("executable = %#v", context.Executable)
	}
	if context.ParentProcess.Status != hook.SignalStatusUnreadable {
		t.Fatalf("parent process = %#v", context.ParentProcess)
	}
	if len(context.CollectionIssues) != 4 {
		t.Fatalf("collection issues = %#v, want four", context.CollectionIssues)
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

func TestRunQuerySeenReportsExpiredDetailAndOmitsContent(t *testing.T) {
	setupQueryEnvironment(t)
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	result, err := store.Append(t.Context(), intake.Record{
		EventID: "evt-cli-expired", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session-expired", EventName: "PreToolUse",
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
		EnvFingerprint:     map[string]string{"AI_AGENT": "codex"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Handle().Exec(`
		delete from intake_event_details
		where event_id = ? and detail_class in (?, ?, ?)
	`, result.EventID, auditstorage.DetailClassNormalizedInput,
		auditstorage.DetailClassProviderEvidence,
		auditstorage.DetailClassEnvironmentEvidence); err != nil {
		t.Fatalf("delete expired detail: %v", err)
	}
	if _, err := store.Handle().Exec(`
		update intake_event_detail_manifest
		set available_classes_json = '["wire_input"]', state = 'expired',
			state_changed_at = '2026-08-12T00:00:00Z'
		where event_id = ?
	`, result.EventID); err != nil {
		t.Fatalf("mark detail expired: %v", err)
	}

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"seen", "--event-id", result.EventID, "--include-normalized", "--include-env", "--json",
	})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, `"detail":{"state":"expired"`) {
		t.Fatalf("expired detail state missing: %s", stdout)
	}
	for _, field := range []string{"classification", "normalized_json", "env_fingerprint"} {
		if strings.Contains(stdout, `"`+field+`"`) {
			t.Fatalf("expired field %q present: %s", field, stdout)
		}
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

func TestRunQueryDecisionsReadsLegacyPayloadSchema(t *testing.T) {
	setupQueryEnvironment(t)
	fixturePath := filepath.Join(
		"..", "..", "internal", "auditstorage", "testdata", "legacy_v1.sql",
	)
	fixture, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatalf("ReadFile legacy fixture: %v", err)
	}
	databasePath := config.DefaultAuditSQLitePath()
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll audit state: %v", err)
	}
	database, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := database.Exec(string(fixture)); err != nil {
		_ = database.Close()
		t.Fatalf("load legacy fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"decisions", "--decision", "block", "--json",
	})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	var record audit.QueryRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &record); err != nil {
		t.Fatalf("decode legacy decision: %v", err)
	}
	if record.EventID != "audit-legacy" {
		t.Fatalf("event id = %q, want audit-legacy", record.EventID)
	}
	if record.Detail.State != auditstorage.DetailStateAvailable {
		t.Fatalf("detail state = %q, want available", record.Detail.State)
	}
	if len(record.Detail.RecordedClasses) != 1 ||
		record.Detail.RecordedClasses[0] != auditstorage.DetailClassDeferredAuditPayload {
		t.Fatalf("recorded classes = %v, want deferred audit payload", record.Detail.RecordedClasses)
	}
}

func TestRunQueryDecisionsRejectsAvailableHeaderWithoutPayload(t *testing.T) {
	setupQueryEnvironment(t)
	record := appendCLIQueryEvaluation(t)
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const auditEventID = "audit-missing-payload"
	if _, err := database.Exec(`
		insert into events (
			event_id, schema_version, time, level, message, system, session_id,
			turn_id, event_name, tool_use_id, tool_name, raw_payload_hash
		) values (?, 1, '2026-07-11T01:00:02Z', 'info', 'hook.blocked',
			'codex', 'session-cli', '', 'PreToolUse', '', 'exec_command', 'sha256:audit')
	`, auditEventID); err != nil {
		t.Fatalf("insert audit event: %v", err)
	}
	if _, err := database.Exec(`
		insert into deferred_audit_outbox (
			receipt_id, event_id, evaluation_id, state, created_at, completed_at,
			claim_owner, claim_expires_at, claim_attempt
		) values (?, ?, ?, 'complete', '2026-07-11T01:00:02Z',
			'2026-07-11T01:00:03Z', null, null, 0)
	`, record.Evaluation.ReceiptID, record.Evaluation.EventID,
		record.Evaluation.EvaluationID); err != nil {
		t.Fatalf("insert audit outbox: %v", err)
	}
	if _, err := database.Exec(`
		insert into deferred_audit_outbox_entries (
			receipt_id, entry_index, audit_event_id, delivered_at,
			payload_recorded, payload_available, payload_state_changed_at
		) values (?, 0, ?, '2026-07-11T01:00:03Z', 1, 1, '2026-07-11T01:00:03Z')
	`, record.Evaluation.ReceiptID, auditEventID); err != nil {
		t.Fatalf("insert audit outbox header: %v", err)
	}

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"decisions", "--json", "--limit", "10",
	})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	var got audit.QueryRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &got); err != nil {
		t.Fatalf("decode audit decision: %v", err)
	}
	if got.Detail.State != auditstorage.DetailStateExpired {
		t.Fatalf("detail state = %q, want expired", got.Detail.State)
	}
	if len(got.Detail.AvailableClasses) != 0 {
		t.Fatalf("available classes = %v, want none", got.Detail.AvailableClasses)
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

func TestExportEvaluationsWritesCompleteDetail(t *testing.T) {
	setupQueryEnvironment(t)
	completedAt := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	record := appendCLIExportEvaluation(
		t,
		"eval-export-complete",
		"evt-export-complete",
		"codex",
		"session-export-complete",
		completedAt,
	)

	exitCode, stdout, stderr := captureRunExport(t, []string{
		"evaluations", "--evaluation-id", record.Evaluation.EvaluationID,
	})

	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, `"evaluation_id":"eval-export-complete"`) {
		t.Fatalf("stdout = %q, want complete export row", stdout)
	}
}

func TestExportEvaluationsRejectsIncompleteDetail(t *testing.T) {
	for _, state := range []auditstorage.DetailState{
		auditstorage.DetailStateExpired,
		auditstorage.DetailStateNotRecorded,
	} {
		t.Run(string(state), func(t *testing.T) {
			setupQueryEnvironment(t)
			record := appendCLIExportEvaluation(
				t,
				"eval-export-"+string(state),
				"evt-export-"+string(state),
				"codex",
				"session-export-"+string(state),
				time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC),
			)
			setCLIExportDetailState(t, record.Evaluation.EvaluationID, state)

			exitCode, stdout, stderr := captureRunExport(t, []string{
				"evaluations", "--evaluation-id", record.Evaluation.EvaluationID,
			})

			if exitCode != 1 || stdout != "" {
				t.Fatalf("exitCode = %d, stdout = %q, stderr = %q", exitCode, stdout, stderr)
			}
			want := "1 selected evaluations lack complete detail; complete detail starts at none"
			if !strings.Contains(stderr, want) {
				t.Fatalf("stderr = %q, want %q", stderr, want)
			}
		})
	}
}

func TestExportEvaluationsRejectsExpiredStateWithDetailRows(t *testing.T) {
	setupQueryEnvironment(t)
	record := appendCLIExportEvaluation(
		t,
		"eval-export-contradictory-expired",
		"evt-export-contradictory-expired",
		"codex",
		"session-export-contradictory-expired",
		time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC),
	)
	setCLIExportStoredDetailState(
		t,
		record.Evaluation.EvaluationID,
		auditstorage.DetailStateExpired,
	)

	exitCode, stdout, stderr := captureRunExport(t, []string{
		"evaluations", "--evaluation-id", record.Evaluation.EvaluationID,
	})

	if exitCode != 1 || stdout != "" {
		t.Fatalf("exitCode = %d, stdout = %q, stderr = %q", exitCode, stdout, stderr)
	}
	want := "1 selected evaluations lack complete detail; complete detail starts at none"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

func TestExportEvaluationsSkipExpiredDetailReportsCount(t *testing.T) {
	setupQueryEnvironment(t)
	complete := appendCLIExportEvaluation(
		t,
		"eval-export-complete",
		"evt-export-complete",
		"claude",
		"session-export-complete",
		time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	)
	expired := appendCLIExportEvaluation(
		t,
		"eval-export-expired",
		"evt-export-expired",
		"codex",
		"session-export-expired",
		time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC),
	)
	notRecorded := appendCLIExportEvaluation(
		t,
		"eval-export-not-recorded",
		"evt-export-not-recorded",
		"gemini",
		"session-export-not-recorded",
		time.Date(2026, 8, 4, 14, 0, 0, 0, time.UTC),
	)
	setCLIExportDetailState(t, expired.Evaluation.EvaluationID, auditstorage.DetailStateExpired)
	setCLIExportDetailState(
		t,
		notRecorded.Evaluation.EvaluationID,
		auditstorage.DetailStateNotRecorded,
	)

	exitCode, stdout, stderr := captureRunExport(t, []string{"evaluations"})
	if exitCode != 1 || stdout != "" {
		t.Fatalf("default exitCode = %d, stdout = %q, stderr = %q", exitCode, stdout, stderr)
	}
	wantDefault := "2 selected evaluations lack complete detail; complete detail starts at 2026-08-04T12:00:00Z"
	if !strings.Contains(stderr, wantDefault) {
		t.Fatalf("default stderr = %q, want %q", stderr, wantDefault)
	}

	exitCode, stdout, stderr = captureRunExport(t, []string{
		"evaluations", "--skip-expired-detail",
	})
	if exitCode != 0 {
		t.Fatalf("skip exitCode = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, `"evaluation_id":"`+complete.Evaluation.EvaluationID+`"`) ||
		strings.Contains(stdout, expired.Evaluation.EvaluationID) ||
		strings.Contains(stdout, notRecorded.Evaluation.EvaluationID) {
		t.Fatalf("skip stdout = %q, want only complete record", stdout)
	}
	if !strings.Contains(stderr, "omitted 2 selected evaluations with incomplete detail") {
		t.Fatalf("skip stderr = %q, want omitted count", stderr)
	}
}

func TestExportEvaluationsSkipsIncompleteDetailBeforePagination(t *testing.T) {
	setupQueryEnvironment(t)
	complete := appendCLIExportEvaluation(
		t,
		"eval-export-older-complete",
		"evt-export-older-complete",
		"claude",
		"session-export-older-complete",
		time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	)
	incomplete := appendCLIExportEvaluation(
		t,
		"eval-export-newer-incomplete",
		"evt-export-newer-incomplete",
		"codex",
		"session-export-newer-incomplete",
		time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC),
	)
	setCLIExportDetailState(
		t,
		incomplete.Evaluation.EvaluationID,
		auditstorage.DetailStateExpired,
	)

	exitCode, stdout, stderr := captureRunExport(t, []string{
		"evaluations", "--skip-expired-detail", "--limit", "1",
	})

	if exitCode != 0 {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, complete.Evaluation.EvaluationID) ||
		strings.Contains(stdout, incomplete.Evaluation.EvaluationID) {
		t.Fatalf("stdout = %q, want only older complete record", stdout)
	}
	if !strings.Contains(stderr, "omitted 1 selected evaluations with incomplete detail") {
		t.Fatalf("stderr = %q, want full selection incomplete count", stderr)
	}
}

func TestExportEvaluationsFiltersBeforeCheckingDetail(t *testing.T) {
	setupQueryEnvironment(t)
	complete := appendCLIExportEvaluation(
		t,
		"eval-export-complete",
		"evt-export-complete",
		"claude",
		"session-export-complete",
		time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC),
	)
	incomplete := appendCLIExportEvaluation(
		t,
		"eval-export-expired",
		"evt-export-expired",
		"codex",
		"session-export-expired",
		time.Date(2026, 8, 4, 13, 0, 0, 0, time.UTC),
	)
	setCLIExportDetailState(t, incomplete.Evaluation.EvaluationID, auditstorage.DetailStateExpired)

	exitCode, stdout, stderr := captureRunExport(t, []string{
		"evaluations", "--system", "claude",
	})

	if exitCode != 0 || stderr != "" {
		t.Fatalf("exitCode = %d, stderr = %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, complete.Evaluation.EvaluationID) ||
		strings.Contains(stdout, incomplete.Evaluation.EvaluationID) {
		t.Fatalf("stdout = %q, want only filtered complete record", stdout)
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

func TestRunQueryEvaluationsTableIgnoresCorruptDetail(t *testing.T) {
	setupQueryEnvironment(t)
	record := appendCLIQueryEvaluation(t)
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.Handle().Exec(`
		update gate_evaluation_layer_details set metadata_json = '{'
		where evaluation_id = ?
	`, record.Evaluation.EvaluationID); err != nil {
		t.Fatalf("corrupt evaluation metadata: %v", err)
	}

	exitCode, stdout, stderr := captureRunQuery(t, []string{
		"evaluations", "--evaluation-id", record.Evaluation.EvaluationID,
	})
	if exitCode != 0 || stderr != "" {
		t.Fatalf("table exitCode = %d, stderr = %q", exitCode, stderr)
	}
	for _, summary := range []string{
		record.Evaluation.EvaluationID,
		"block",
		"available",
	} {
		if !strings.Contains(stdout, summary) {
			t.Fatalf("table missing summary %q: %s", summary, stdout)
		}
	}

	exitCode, _, stderr = captureRunQuery(t, []string{
		"evaluations", "--evaluation-id", record.Evaluation.EvaluationID, "--json",
	})
	if exitCode != 1 || !strings.Contains(stderr, "metadata") {
		t.Fatalf("JSON exitCode = %d, stderr = %q, want strict metadata failure", exitCode, stderr)
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

func TestQueryTableRenderersPreserveColumnsAndAddDetail(t *testing.T) {
	seen := intake.QueryResult{
		Source: "sqlite",
		Records: []intake.QueryRecord{
			{
				RecordedAt: "2026-07-11T01:02:03Z", System: "codex",
				SessionID: "session-1", EventName: "PreToolUse", ToolName: "Shell",
				Operation: intake.Operation{Command: "make test"},
				Deferred:  intake.QueryDeferred{State: intake.DeferredStatePending},
				Detail: auditstorage.DetailProjection{
					State: auditstorage.DetailStateProtected,
				},
			},
		},
	}
	seenOutput := captureStdoutCall(t, func() { printSeenTable(seen) })
	wantSeen := "source=sqlite rows=1\n" +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %-12s  %s\n", "recorded_at", "system", "state", "event", "tool", "session", "detail", "command") +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %-12s  %s\n", "2026-07-11T01:02:03Z", "codex", "pending", "PreToolUse", "Shell", "session-1", "protected", "make test")
	if seenOutput != wantSeen {
		t.Fatalf("seen table changed\ngot:  %q\nwant: %q", seenOutput, wantSeen)
	}

	events := []audit.QueryRecord{
		{
			Event: audit.Event{
				Time: "2026-07-11T01:02:03Z", System: "codex", EventName: "PreToolUse",
				ToolName: "Shell", Operation: audit.Operation{Command: "make test"},
				Decision: audit.Decision{Kind: "block", RulesMatched: []string{"rule-1"}},
			},
			Detail: auditstorage.DetailProjection{State: auditstorage.DetailStateAvailable},
		},
	}
	eventOutput := captureStdoutCall(t, func() { printEventTable("sqlite", events) })
	wantEvent := "source=sqlite rows=1\n" +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %-12s  %s\n", "time", "system", "decision", "event", "tool", "rules", "detail", "command") +
		fmt.Sprintf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %-12s  %s\n", "2026-07-11T01:02:03Z", "codex", "block", "PreToolUse", "Shell", "rule-1", "available", "make test")
	if eventOutput != wantEvent {
		t.Fatalf("decision table changed\ngot:  %q\nwant: %q", eventOutput, wantEvent)
	}
}

func appendCLIQueryEvaluation(t *testing.T) evaluation.Record {
	return appendCLIExportEvaluation(
		t,
		"eval-cli",
		"evt-cli-evaluation",
		"codex",
		"session-cli",
		time.Date(2026, 7, 11, 1, 0, 1, 0, time.UTC).Add(time.Millisecond),
	)
}

func appendCLIExportEvaluation(
	t *testing.T,
	evaluationID string,
	eventID string,
	system string,
	sessionID string,
	completedAt time.Time,
) evaluation.Record {
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
		EventID: eventID, RecordedAt: completedAt.Add(-time.Second),
		System: system, SessionID: sessionID, EventName: "PreToolUse",
		ToolName: "exec_command", RawPayload: []byte(`{"authorization":"raw"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	startedAt := completedAt.Add(-time.Millisecond)
	record := evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: evaluationID, ReceiptID: receipt.ReceiptID, EventID: receipt.EventID,
			Attempt: 1, Mode: "hot", ConfigHash: "sha256:config", EngineVersion: "v1",
			EngineCommit: "commit", EngineBuildHash: "sha256:build", InputHash: "sha256:input",
			StartedAt: startedAt, CompletedAt: completedAt,
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

func setCLIExportDetailState(
	t *testing.T,
	evaluationID string,
	state auditstorage.DetailState,
) {
	t.Helper()
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close audit database: %v", err)
		}
	})
	for _, table := range []string{
		"gate_evaluation_label_details",
		"gate_evaluation_layer_details",
		"gate_evaluation_details",
	} {
		if _, err := database.Exec(
			"delete from "+table+" where evaluation_id = ?",
			evaluationID,
		); err != nil {
			t.Fatalf("delete %s: %v", table, err)
		}
	}
	setCLIExportStoredDetailStateWithDatabase(t, database, evaluationID, state)
}

func setCLIExportStoredDetailState(
	t *testing.T,
	evaluationID string,
	state auditstorage.DetailState,
) {
	t.Helper()
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close audit database: %v", err)
		}
	})
	setCLIExportStoredDetailStateWithDatabase(t, database, evaluationID, state)
}

func setCLIExportStoredDetailStateWithDatabase(
	t *testing.T,
	database *sql.DB,
	evaluationID string,
	state auditstorage.DetailState,
) {
	t.Helper()
	if _, err := database.Exec(
		`update gate_evaluations set detail_state = ? where evaluation_id = ?`,
		state,
		evaluationID,
	); err != nil {
		t.Fatalf("mark evaluation detail %s: %v", state, err)
	}
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
			return "test-working-directory", nil
		},
		executable: func() (string, error) {
			return "agent-gate", nil
		},
		processes: func() (hook.ProcessEvidence, []hook.ProcessEvidence, error) {
			return hook.ProcessEvidence{
				Name: "shell", ExecutablePath: "shell", Source: "parent_process",
				Provenance: "operating_system", Status: hook.SignalStatusObserved,
			}, nil, nil
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

func captureRunExport(t *testing.T, args []string) (int, string, string) {
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

	exitCode := runExport(args)
	stdout := readCapturedFile(t, stdoutFile)
	stderr := readCapturedFile(t, stderrFile)
	return exitCode, stdout, stderr
}

func TestRunConfigCheckRejectsHookSchemaViolation(t *testing.T) {
	setupQueryEnvironment(t)
	configPath := config.Path()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	body := `
[[rules]]
name = "invalid-temporal-selector"
cursor_events = ["stop"]
action = "block"
field_paths = ["last_user_message"]
pattern = "blocked"
violation_message = "blocked"
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	exitCode, stdout, stderr := captureRunConfig(t, []string{"check"})
	if exitCode != 1 {
		t.Fatalf("runConfig() exit code = %d, want 1", exitCode)
	}
	if stdout != "" {
		t.Fatalf("runConfig() stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "valid only in exec condition field_paths") {
		t.Fatalf("runConfig() stderr = %q, want temporal selector scope error", stderr)
	}
}

func TestRunConfigCheckPrintsEffectiveAuditStoragePolicy(t *testing.T) {
	setupQueryEnvironment(t)
	configPath := config.Path()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[audit]\nenabled = true\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	exitCode, stdout, stderr := captureRunConfig(t, []string{"check"})
	if exitCode != 0 {
		t.Fatalf("runConfig() exit code = %d, want 0; stderr = %q", exitCode, stderr)
	}
	want := "" +
		"agent-gate: config ok\n" +
		"audit storage: balanced\n" +
		"full detail: 168h0m0s\n" +
		"summary: 720h0m0s\n" +
		"size target: disabled\n" +
		"maintenance: every 24h0m0s, 1000 rows per batch\n"
	if stdout != want {
		t.Fatalf("runConfig() stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("runConfig() stderr = %q, want empty", stderr)
	}
}

func TestRunConfigCheckPrintsConfiguredAuditStorageSize(t *testing.T) {
	setupQueryEnvironment(t)
	configPath := config.Path()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	body := `
[audit.storage]
profile = "full"
maintenance_interval = "12h"
max_size_mb = 25
maintenance_batch_rows = 123
`
	if err := os.WriteFile(configPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	exitCode, stdout, stderr := captureRunConfig(t, []string{"check"})
	if exitCode != 0 {
		t.Fatalf("runConfig() exit code = %d, want 0; stderr = %q", exitCode, stderr)
	}
	want := "" +
		"agent-gate: config ok\n" +
		"audit storage: full\n" +
		"full detail: 720h0m0s\n" +
		"summary: 720h0m0s\n" +
		"size target: 25000000 bytes\n" +
		"maintenance: every 12h0m0s, 123 rows per batch\n"
	if stdout != want {
		t.Fatalf("runConfig() stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Fatalf("runConfig() stderr = %q, want empty", stderr)
	}
}

func TestRunConfigCheckRejectsInvalidAuditStorage(t *testing.T) {
	setupQueryEnvironment(t)
	configPath := config.Path()
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	if err := os.WriteFile(configPath, []byte("[audit.storage]\nmax_size_mb = -1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}

	exitCode, stdout, stderr := captureRunConfig(t, []string{"check"})
	if exitCode != 1 {
		t.Fatalf("runConfig() exit code = %d, want 1", exitCode)
	}
	if stdout != "" {
		t.Fatalf("runConfig() stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "audit.storage.max_size_mb must not be negative") {
		t.Fatalf("runConfig() stderr = %q, want audit storage validation error", stderr)
	}
}

func captureRunConfig(t *testing.T, args []string) (int, string, string) {
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

	exitCode := runConfig(args)
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
