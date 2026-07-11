package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

type deferredLedgerStore struct {
	intakeStore
	record       intake.Record
	order        *[]string
	completeErr  error
	replayRecord intake.Record
}

func (store *deferredLedgerStore) GetReceipt(context.Context, int64) (intake.Record, error) {
	return store.record, nil
}

func (store *deferredLedgerStore) MarkDeferredComplete(context.Context, int64) error {
	*store.order = append(*store.order, "complete")
	return store.completeErr
}

func (store *deferredLedgerStore) ReplayPending(
	_ context.Context,
	replay func(intake.Record) error,
) error {
	return replay(store.replayRecord)
}

type orderedDeferredRecorder struct {
	mu      sync.Mutex
	order   *[]string
	records []evaluation.Record
	err     error
}

func (recorder *orderedDeferredRecorder) RecordCompleted(
	_ context.Context,
	record evaluation.Record,
) error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	*recorder.order = append(*recorder.order, "ledger")
	if recorder.err != nil {
		return recorder.err
	}
	recorder.records = append(recorder.records, record)
	return nil
}

type orderedDurableAuditSink struct {
	order *[]string
	err   error
}

func (sink *orderedDurableAuditSink) Log(
	context.Context,
	string,
	string,
	string,
	string,
	string,
	audit.Attrs,
) {
}

func (sink *orderedDurableAuditSink) LogDurable(
	_ context.Context,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	_ audit.Attrs,
) error {
	*sink.order = append(*sink.order, "audit")
	return sink.err
}

func (sink *orderedDurableAuditSink) Close() error {
	return nil
}

func TestDeferredImmediateEvaluationUsesReceiptAndCommitsBeforeCompletion(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(41, 0)
	store := &deferredLedgerStore{record: record, order: &order}
	recorder := &orderedDeferredRecorder{order: &order, records: nil}
	processor := deferredLedgerProcessor(t, store, recorder, &orderedDurableAuditSink{order: &order})
	hotEvent := deferredLedgerHotEvent(t, processor.cfg, record)

	processor.processRecord(context.Background(), record, &hotEvent)

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	got := recorder.records[0]
	if got.Evaluation.ReceiptID != 41 || got.Evaluation.Mode != "deferred" ||
		got.Evaluation.Attempt != 1 {
		t.Fatalf("evaluation identity = %+v", got.Evaluation)
	}
	if len(got.Layers) != 2 || got.Layers[0].Kind != "deterministic" ||
		got.Layers[1].Name != "audit-result" {
		t.Fatalf("deferred layers = %+v", got.Layers)
	}
	wantOrder := "ledger,audit,audit,complete"
	if joined := joinDeferredOrder(order); joined != wantOrder {
		t.Fatalf("order = %q, want %q", joined, wantOrder)
	}
}

func TestDeferredReplayEvaluationUsesNextAttempt(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(44, 1)
	store := &deferredLedgerStore{record: record, order: &order, replayRecord: record}
	recorder := &orderedDeferredRecorder{order: &order, records: nil}
	processor := deferredLedgerProcessor(t, store, recorder, &orderedDurableAuditSink{order: &order})

	if err := processor.ReplayPending(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(recorder.records) != 1 {
		t.Fatalf("records = %d, want 1", len(recorder.records))
	}
	got := recorder.records[0].Evaluation
	if got.ReceiptID != 44 || got.Mode != "deferred_replay" || got.Attempt != 2 {
		t.Fatalf("evaluation identity = %+v", got)
	}
}

func TestDeferredLedgerFailureLeavesReceiptPending(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(42, 0)
	store := &deferredLedgerStore{record: record, order: &order}
	recorder := &orderedDeferredRecorder{
		order: &order, records: nil, err: errors.New("ledger unavailable"),
	}
	processor := deferredLedgerProcessor(t, store, recorder, &orderedDurableAuditSink{order: &order})
	hotEvent := deferredLedgerHotEvent(t, processor.cfg, record)

	processor.processRecord(context.Background(), record, &hotEvent)

	if joined := joinDeferredOrder(order); joined != "ledger" {
		t.Fatalf("order = %q, want ledger only", joined)
	}
}

func TestDeferredAuditFailureLeavesReceiptPending(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(43, 0)
	store := &deferredLedgerStore{record: record, order: &order}
	recorder := &orderedDeferredRecorder{order: &order, records: nil}
	sink := &orderedDurableAuditSink{order: &order, err: errors.New("audit unavailable")}
	processor := deferredLedgerProcessor(t, store, recorder, sink)
	hotEvent := deferredLedgerHotEvent(t, processor.cfg, record)

	processor.processRecord(context.Background(), record, &hotEvent)

	if joined := joinDeferredOrder(order); joined != "ledger,audit" {
		t.Fatalf("order = %q, want ledger,audit", joined)
	}
}

func deferredLedgerProcessor(
	t *testing.T,
	store intakeStore,
	recorder evaluationRecorder,
	sink audit.Sink,
) *deferredProcessor {
	t.Helper()
	processor := newDeferredProcessor(
		context.Background(), store, sink, auditOnlyDaemonTestConfig(t), nil, 1, 0,
		newDiscardLogger(),
	)
	processor.evaluationRecorder = recorder
	t.Cleanup(processor.Close)
	return processor
}

func deferredLedgerRecord(receiptID int64, replayCount int) intake.Record {
	return intake.Record{
		ReceiptID: receiptID, EventID: "deferred-event", System: "codex",
		SessionID: "session", EventName: "PreToolUse",
		RawPayload:      []byte(`{"session_id":"session","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		NormalizedJSON:  []byte(`{"session_id":"session","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo ok"}}`),
		EnvFingerprint:  map[string]string{},
		DeferredState:   intake.DeferredStatePending,
		DeferredReplays: replayCount,
	}
}

func deferredLedgerHotEvent(
	t *testing.T,
	cfg *config.Config,
	record intake.Record,
) hook.DeferredAuditEvent {
	t.Helper()
	return hook.EvaluateHotWithEventID(
		context.Background(), record.RawPayload, hook.SyncConfig(cfg),
		hook.SystemCodex, func(string) string { return "" }, record.EventID,
	).Deferred
}

func joinDeferredOrder(values []string) string {
	return strings.Join(values, ",")
}

var _ audit.Sink = (*orderedDurableAuditSink)(nil)
