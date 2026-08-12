package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestEvaluateHookMinimalDetailProtectsPayloadThroughDeferredDelivery(t *testing.T) {
	setDaemonTestDirs(t)
	databasePath := filepath.Join(t.TempDir(), "audit.db")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configBody := `[audit]
enabled = true

[audit.storage]
profile = "minimal"

[audit.outputs.sqlite]
path = "` + databasePath + `"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	snapshot := server.runtime.Load()
	originalProcessor := snapshot.deferredProcessor
	originalProcessor.Close()
	controlledProcessor := newDeferredProcessor(
		context.Background(), snapshot.intakeStore, originalProcessor.sink, cfg,
		snapshot.inferRuntime, 1, 0, newDiscardLogger(),
	)
	controlledProcessor.evaluationRecorder = snapshot.evaluationRecorder
	snapshot.deferredProcessor = controlledProcessor

	request := blockingLedgerRequest(t)
	if _, err := server.EvaluateHook(t.Context(), request); err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	var work deferredWork
	select {
	case work = <-controlledProcessor.events:
	case <-time.After(time.Second):
		t.Fatal("deferred work was not enqueued")
	}
	sqliteStore, ok := snapshot.intakeStore.(*sqliteIntakeStore)
	if !ok {
		t.Fatalf("intake store type = %T", snapshot.intakeStore)
	}
	before, err := sqliteStore.GetReceipt(t.Context(), work.receiptID)
	if err != nil {
		t.Fatalf("GetReceipt before deferred delivery: %v", err)
	}
	if !bytes.Equal(before.RawPayload, request.RawJson) {
		t.Fatalf("protected raw payload = %q, want %q", before.RawPayload, request.RawJson)
	}
	var state auditstorage.DetailState
	if err := sqliteStore.Handle().QueryRowContext(t.Context(), `
		select state from intake_event_detail_manifest where event_id = ?
	`, work.eventID).Scan(&state); err != nil {
		t.Fatalf("query daemon detail manifest: %v", err)
	}
	if state != auditstorage.DetailStateProtected {
		t.Fatalf("daemon detail state = %q, want protected", state)
	}

	controlledProcessor.processEvent(t.Context(), work)
	after, err := sqliteStore.GetReceipt(t.Context(), work.receiptID)
	if err != nil {
		t.Fatalf("GetReceipt after deferred delivery: %v", err)
	}
	if !bytes.Equal(after.RawPayload, request.RawJson) {
		t.Fatalf("delivered raw payload = %q, want %q", after.RawPayload, request.RawJson)
	}
	pendingAudit, err := sqliteStore.store.ListPendingDeferredAudit(t.Context(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	if len(pendingAudit) != 0 {
		t.Fatalf("pending audit receipts = %v, want none", pendingAudit)
	}
}

func TestEvaluateHookClosedInferenceErrorBlocksAndPersistsValidLayer(t *testing.T) {
	setDaemonTestDirs(t)
	fake := newDeferredInferenceFake(`{`)
	endpoint := startDeferredInferenceServer(t, fake)
	cfg := loadDeferredInferConfig(t, endpoint)
	cfg.Rules[0].Conditions[0].OnError = "closed"
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	server.runtime.Load().evaluationRecorder = recorder

	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if len(response.StdoutData) == 0 {
		t.Fatalf("closed inference response = %+v", response)
	}
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Evaluation.FinalVerdict != "block" ||
		records[0].Evaluation.FinalSource != "inference" || !records[0].Evaluation.Enforced {
		t.Fatalf("records = %+v", records)
	}
	var inferenceLayerFound bool
	for _, layer := range records[0].Layers {
		if layer.Kind != "inference" {
			continue
		}
		inferenceLayerFound = true
		if layer.Status != "error" || layer.ErrorCode != "invalid_response" ||
			!json.Valid(layer.OutputJSON) || strings.Contains(string(layer.OutputJSON), `"raw"`) ||
			!strings.Contains(string(layer.OutputJSON), `"byte_length":1`) ||
			!strings.Contains(string(layer.OutputJSON), `"sha256":"sha256:`) {
			t.Fatalf("inference layer = %+v", layer)
		}
	}
	if !inferenceLayerFound {
		t.Fatalf("inference layer missing: %+v", records[0].Layers)
	}
}

type recordingEvaluationRecorder struct {
	mu      sync.Mutex
	records []evaluation.Record
	err     error
	hotErr  error
	started chan struct{}
	release chan struct{}
}

func (recorder *recordingEvaluationRecorder) RecordCompleted(
	_ context.Context,
	record evaluation.Record,
) error {
	if recorder.started != nil {
		close(recorder.started)
	}
	if recorder.release != nil {
		<-recorder.release
	}
	if recorder.err != nil {
		return recorder.err
	}
	recorder.mu.Lock()
	recorder.records = append(recorder.records, record)
	recorder.mu.Unlock()
	return nil
}

func (recorder *recordingEvaluationRecorder) CommitHotEvaluation(
	ctx context.Context,
	_ string,
	_ int64,
	_ bool,
	record evaluation.Record,
) error {
	if recorder.hotErr != nil {
		return recorder.hotErr
	}
	return recorder.RecordCompleted(ctx, record)
}

func (recorder *recordingEvaluationRecorder) CommitDeferredEvaluation(
	ctx context.Context,
	_ intake.DeferredClaim,
	record evaluation.Record,
	_ []audit.NormalizedEntry,
) error {
	return recorder.RecordCompleted(ctx, record)
}

func (recorder *recordingEvaluationRecorder) snapshot() []evaluation.Record {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]evaluation.Record(nil), recorder.records...)
}

func TestEvaluateHookEvaluationCommitPrecedesBlockingResponse(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil,
		started: make(chan struct{}), release: make(chan struct{}),
	}
	server.runtime.Load().evaluationRecorder = recorder
	responses := make(chan *daemonpb.EvaluateHookResponse, 1)
	go func() {
		response, _ := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
		responses <- response
	}()
	<-recorder.started
	select {
	case response := <-responses:
		t.Fatalf("blocking response escaped before evaluation commit: %+v", response)
	default:
	}
	close(recorder.release)
	response := <-responses
	if len(response.StdoutData) == 0 {
		t.Fatalf("response = %+v, want blocking response", response)
	}
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Evaluation.FinalVerdict != "block" ||
		!records[0].Evaluation.Enforced {
		t.Fatalf("records = %+v", records)
	}
}

func TestEvaluateHookLedgerFailureReturnsFailOpen(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	server.runtime.Load().evaluationRecorder = &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: errors.New("ledger unavailable"),
		started: nil, release: nil,
	}
	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	// Still an allow, and now it says a verdict was computed and discarded.
	// The discarded verdict may have been a block, so presenting it as a clean
	// allow would show a dropped block as compliance.
	if response.ExitCode != 0 {
		t.Fatalf("ledger failure exit = %d, want 0", response.ExitCode)
	}
	assertSaysUnevaluated(t, response, hook.FailOpenReasonVerdictNotRecorded)
}

func TestEvaluateHookAtomicHotCommitFailureRecordsAndReturnsFailOpen(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	snapshot := server.runtime.Load()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil,
		hotErr: errors.New("atomic hot commit unavailable"), started: nil, release: nil,
	}
	snapshot.evaluationRecorder = recorder
	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	// Still an allow, and now it says a verdict was computed and discarded.
	// The discarded verdict may have been a block, so presenting it as a clean
	// allow would show a dropped block as compliance.
	if response.ExitCode != 0 {
		t.Fatalf("pending failure exit = %d, want 0", response.ExitCode)
	}
	assertSaysUnevaluated(t, response, hook.FailOpenReasonVerdictNotRecorded)
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Evaluation.FinalVerdict != "error" ||
		records[0].Evaluation.EnforcementAction != "fail_open" {
		t.Fatalf("records = %+v", records)
	}
}

func TestEvaluateHookFallbackLedgerFailureLogsDistinctStatus(t *testing.T) {
	setDaemonTestDirs(t)
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	server, err := New(logger, daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	server.runtime.Load().evaluationRecorder = &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil,
		err:    errors.New("fallback ledger unavailable"),
		hotErr: errors.New("atomic hot commit unavailable"), started: nil, release: nil,
	}

	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	// Still an allow, and now it says a verdict was computed and discarded.
	// The discarded verdict may have been a block, so presenting it as a clean
	// allow would show a dropped block as compliance.
	if response.ExitCode != 0 {
		t.Fatalf("fallback failure exit = %d, want 0", response.ExitCode)
	}
	assertSaysUnevaluated(t, response, hook.FailOpenReasonVerdictNotRecorded)
	if !strings.Contains(logs.String(), "status_class=fallback_evaluation_persistence_failed") {
		t.Fatalf("logs missing fallback persistence status: %s", logs.String())
	}
}

func TestEvaluateHookDuplicateReceiptsCreateDistinctEvaluations(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	server.runtime.Load().evaluationRecorder = recorder
	request := blockingLedgerRequest(t)
	for range 2 {
		response, evalErr := server.EvaluateHook(context.Background(), request)
		if evalErr != nil || len(response.StdoutData) == 0 {
			t.Fatalf("EvaluateHook response/error = %+v/%v", response, evalErr)
		}
	}
	records := recorder.snapshot()
	if len(records) != 2 || records[0].Evaluation.EventID != records[1].Evaluation.EventID ||
		records[0].Evaluation.ReceiptID == records[1].Evaluation.ReceiptID ||
		records[0].Evaluation.EvaluationID == records[1].Evaluation.EvaluationID {
		t.Fatalf("duplicate records = %+v", records)
	}
}

func TestEvaluateHookAllowPersistsEvaluation(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	server.runtime.Load().evaluationRecorder = recorder
	request := blockingLedgerRequest(t)
	request.RawJson = []byte(`{"session_id":"ledger-session","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`)
	response, err := server.EvaluateHook(context.Background(), request)
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 0 || len(response.StderrData) != 0 {
		t.Fatalf("allow response = %+v", response)
	}
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Evaluation.FinalVerdict != "allow" ||
		records[0].Evaluation.EnforcementAction != "allow" || records[0].Evaluation.Enforced {
		t.Fatalf("allow records = %+v", records)
	}
}

func TestEvaluateHookParseFailurePersistsValidationEvaluation(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	server.runtime.Load().evaluationRecorder = recorder

	response, err := server.EvaluateHook(
		context.Background(),
		&daemonpb.EvaluateHookRequest{RawJson: []byte(`{`), ProviderHint: "codex"},
	)
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if response.ExitCode != 2 {
		t.Fatalf("exit_code = %d, want 2", response.ExitCode)
	}
	records := recorder.snapshot()
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.Evaluation.ReceiptID <= 0 || record.Evaluation.FinalVerdict != "error" ||
		record.Evaluation.FinalSource != "input_validation" ||
		record.Evaluation.EnforcementAction != "reject_invalid" ||
		!record.Evaluation.Enforced {
		t.Fatalf("parse evaluation = %+v", record.Evaluation)
	}
	if len(record.Layers) != 2 || record.Layers[0].Kind != "validation" ||
		record.Layers[0].Name != "payload-parse" || record.Layers[0].Status != "error" ||
		record.Layers[0].ErrorCode != "intake_parse_failed" {
		t.Fatalf("parse layers = %+v", record.Layers)
	}
}

func TestEvaluateHookQueueSaturationAfterEvaluationDoesNotChangeVerdict(t *testing.T) {
	setDaemonTestDirs(t)
	server, err := New(newDiscardLogger(), daemonTestConfig(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	snapshot := server.runtime.Load()
	recorder := &recordingEvaluationRecorder{
		mu: sync.Mutex{}, records: nil, err: nil, started: nil, release: nil,
	}
	snapshot.evaluationRecorder = recorder
	originalProcessor := snapshot.deferredProcessor
	saturatedProcessor := newDeferredProcessor(
		context.Background(),
		snapshot.intakeStore,
		nil,
		snapshot.cfg,
		snapshot.inferRuntime,
		1,
		0,
		newDiscardLogger(),
	)
	saturatedProcessor.events <- deferredWork{}
	snapshot.deferredProcessor = saturatedProcessor
	defer func() {
		saturatedProcessor.Close()
		snapshot.deferredProcessor = originalProcessor
	}()
	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}
	if len(response.StdoutData) == 0 {
		t.Fatalf("queue saturation changed blocking response: %+v", response)
	}
	records := recorder.snapshot()
	if len(records) != 1 || records[0].Evaluation.FinalVerdict != "block" {
		t.Fatalf("queue saturation records = %+v", records)
	}
}

func blockingLedgerRequest(t *testing.T) *daemonpb.EvaluateHookRequest {
	t.Helper()
	return &daemonpb.EvaluateHookRequest{
		RawJson:      []byte(`{"session_id":"ledger-session","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint: "codex", Cwd: t.TempDir(),
		EnvFingerprint: map[string]string{"CODEX_THREAD_ID": "ledger-thread"},
	}
}

var _ evaluationRecorder = (*recordingEvaluationRecorder)(nil)
