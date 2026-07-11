package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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
	renewed      chan struct{}
	releaseRenew chan struct{}
	renewErr     error
}

func (store *deferredLedgerStore) GetReceipt(context.Context, int64) (intake.Record, error) {
	return store.record, nil
}

func (store *deferredLedgerStore) ClaimDeferred(
	context.Context,
	int64,
	string,
	time.Duration,
) (intake.Record, intake.DeferredClaim, error) {
	record := store.replayRecord
	claim := intake.DeferredClaim{
		ReceiptID: record.ReceiptID, EventID: record.EventID,
		Owner: "test-owner", Attempt: record.DeferredReplays + 1,
		ExpiresAt: time.Now().Add(time.Minute),
	}
	return record, claim, nil
}

func (store *deferredLedgerStore) ReleaseDeferredClaim(
	context.Context,
	intake.DeferredClaim,
) error {
	*store.order = append(*store.order, "release")
	return nil
}

func (store *deferredLedgerStore) RenewDeferredClaim(
	context.Context,
	intake.DeferredClaim,
	time.Duration,
) error {
	if store.renewed != nil {
		select {
		case store.renewed <- struct{}{}:
		default:
		}
	}
	if store.releaseRenew != nil {
		<-store.releaseRenew
	}
	return store.renewErr
}

func (store *deferredLedgerStore) ListPending(context.Context) ([]int64, error) {
	return []int64{store.replayRecord.ReceiptID}, nil
}

type orderedDeferredRecorder struct {
	mu            sync.Mutex
	order         *[]string
	records       []evaluation.Record
	err           error
	commitStarted chan struct{}
	releaseCommit chan struct{}
	panicOnCommit bool
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

func (recorder *orderedDeferredRecorder) CommitHotEvaluation(
	ctx context.Context,
	_ string,
	_ int64,
	_ bool,
	record evaluation.Record,
) error {
	return recorder.RecordCompleted(ctx, record)
}

func (recorder *orderedDeferredRecorder) CommitDeferredEvaluation(
	_ context.Context,
	_ intake.DeferredClaim,
	record evaluation.Record,
) error {
	recorder.mu.Lock()
	*recorder.order = append(*recorder.order, "commit")
	recorder.mu.Unlock()
	if recorder.commitStarted != nil {
		recorder.commitStarted <- struct{}{}
	}
	if recorder.releaseCommit != nil {
		<-recorder.releaseCommit
	}
	if recorder.panicOnCommit {
		panic("forced deferred commit panic")
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.err != nil {
		return recorder.err
	}
	recorder.records = append(recorder.records, record)
	return nil
}

func TestDeferredEvaluationRenewsClaimWhileProcessing(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(45, 0)
	renewed := make(chan struct{}, 1)
	store := &deferredLedgerStore{
		record: record, order: &order, replayRecord: record, renewed: renewed,
	}
	commitStarted := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	recorder := &orderedDeferredRecorder{
		order: &order, records: nil, commitStarted: commitStarted,
		releaseCommit: releaseCommit,
	}
	processor := deferredLedgerProcessor(
		t, store, recorder, &orderedDurableAuditSink{order: &order},
	)
	processor.claimLease = 60 * time.Millisecond
	processor.claimRenewInterval = 10 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		processor.processEvent(context.Background(), deferredWork{receiptID: record.ReceiptID})
	}()

	select {
	case <-commitStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred evaluation did not reach commit")
	}
	select {
	case <-renewed:
	case <-time.After(time.Second):
		t.Fatal("deferred claim was not renewed while evaluation remained active")
	}
	close(releaseCommit)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deferred evaluation did not finish")
	}
}

func TestDeferredCommitStopsRenewalBeforeAudit(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(46, 0)
	renewed := make(chan struct{}, 1)
	releaseRenew := make(chan struct{})
	store := &deferredLedgerStore{
		record: record, order: &order, replayRecord: record, renewed: renewed,
		releaseRenew: releaseRenew, renewErr: intake.ErrDeferredClaimLost,
	}
	commitStarted := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	recorder := &orderedDeferredRecorder{
		order: &order, records: nil, commitStarted: commitStarted,
		releaseCommit: releaseCommit,
	}
	auditContextErrors := make(chan error, 1)
	sink := &orderedDurableAuditSink{order: &order, contextErrors: auditContextErrors}
	processor := deferredLedgerProcessor(t, store, recorder, sink)
	processor.claimLease = 60 * time.Millisecond
	processor.claimRenewInterval = 10 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		processor.processEvent(context.Background(), deferredWork{receiptID: record.ReceiptID})
	}()

	select {
	case <-commitStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred evaluation did not reach commit")
	}
	select {
	case <-renewed:
	case <-time.After(time.Second):
		t.Fatal("deferred claim renewal did not overlap commit")
	}
	close(releaseCommit)
	close(releaseRenew)
	select {
	case err := <-auditContextErrors:
		if err != nil {
			t.Fatalf("audit context error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred audit was not written")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deferred evaluation did not finish")
	}
}

func TestDeferredPanicStopsClaimRenewal(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(47, 0)
	renewed := make(chan struct{}, 1)
	store := &deferredLedgerStore{
		record: record, order: &order, replayRecord: record, renewed: renewed,
	}
	commitStarted := make(chan struct{}, 1)
	releaseCommit := make(chan struct{})
	recorder := &orderedDeferredRecorder{
		order: &order, records: nil, commitStarted: commitStarted,
		releaseCommit: releaseCommit, panicOnCommit: true,
	}
	processor := deferredLedgerProcessor(
		t, store, recorder, &orderedDurableAuditSink{order: &order},
	)
	processor.claimLease = 60 * time.Millisecond
	processor.claimRenewInterval = 10 * time.Millisecond
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			_ = recover()
		}()
		processor.processEvent(context.Background(), deferredWork{receiptID: record.ReceiptID})
	}()

	select {
	case <-commitStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred evaluation did not reach commit")
	}
	select {
	case <-renewed:
	case <-time.After(time.Second):
		t.Fatal("deferred claim was not renewed before panic")
	}
	close(releaseCommit)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("panicking deferred evaluation did not unwind")
	}
	select {
	case <-renewed:
	default:
	}
	select {
	case <-renewed:
		t.Fatal("deferred claim renewed after processing panic")
	case <-time.After(3 * processor.claimRenewInterval):
	}
}

type orderedDurableAuditSink struct {
	order         *[]string
	err           error
	contextErrors chan error
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
	ctx context.Context,
	_ string,
	_ string,
	_ string,
	_ string,
	_ string,
	_ audit.Attrs,
) error {
	*sink.order = append(*sink.order, "audit")
	if sink.contextErrors != nil {
		sink.contextErrors <- ctx.Err()
	}
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

	processor.processRecord(
		context.Background(), context.Background(), record, deferredLedgerClaim(record), &hotEvent, nil,
	)

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
	wantOrder := "commit,audit,audit"
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

	processor.processRecord(
		context.Background(), context.Background(), record, deferredLedgerClaim(record), &hotEvent, nil,
	)

	if joined := joinDeferredOrder(order); joined != "commit,release" {
		t.Fatalf("order = %q, want commit,release", joined)
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

	processor.processRecord(
		context.Background(), context.Background(), record, deferredLedgerClaim(record), &hotEvent, nil,
	)

	if joined := joinDeferredOrder(order); joined != "commit,audit" {
		t.Fatalf("order = %q, want commit,audit", joined)
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

func deferredLedgerClaim(record intake.Record) intake.DeferredClaim {
	return intake.DeferredClaim{
		ReceiptID: record.ReceiptID, EventID: record.EventID,
		Owner: "test-owner", Attempt: record.DeferredReplays + 1,
		ExpiresAt: time.Now().Add(time.Minute),
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
