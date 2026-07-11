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
	auditEntries []intake.DeferredAuditEntry
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

func (store *deferredLedgerStore) ListPendingDeferredAudit(
	context.Context,
	int,
) ([]int64, error) {
	if len(store.auditEntries) == 0 {
		return nil, nil
	}
	return []int64{store.replayRecord.ReceiptID}, nil
}

func (store *deferredLedgerStore) ClaimDeferredAudit(
	_ context.Context,
	receiptID int64,
	owner string,
	lease time.Duration,
) ([]intake.DeferredAuditEntry, intake.DeferredAuditClaim, error) {
	if len(store.auditEntries) == 0 {
		return nil, intake.DeferredAuditClaim{}, intake.ErrDeferredAuditClaimUnavailable
	}
	claim := intake.DeferredAuditClaim{
		ReceiptID: receiptID, EventID: store.replayRecord.EventID, Owner: owner,
		Attempt: 1, ExpiresAt: time.Now().Add(lease),
	}
	return append([]intake.DeferredAuditEntry(nil), store.auditEntries...), claim, nil
}

func (store *deferredLedgerStore) RenewDeferredAuditClaim(
	context.Context,
	intake.DeferredAuditClaim,
	time.Duration,
) error {
	return nil
}

func (store *deferredLedgerStore) MarkDeferredAuditEntryDelivered(
	_ context.Context,
	_ intake.DeferredAuditClaim,
	entryIndex int,
) error {
	for i := range store.auditEntries {
		if store.auditEntries[i].Index == entryIndex {
			store.auditEntries = append(store.auditEntries[:i], store.auditEntries[i+1:]...)
			return nil
		}
	}
	return intake.ErrDeferredAuditClaimLost
}

func (store *deferredLedgerStore) CompleteDeferredAudit(
	context.Context,
	intake.DeferredAuditClaim,
) error {
	if len(store.auditEntries) != 0 {
		return intake.ErrDeferredAuditClaimLost
	}
	return nil
}

func (store *deferredLedgerStore) ReleaseDeferredAuditClaim(
	context.Context,
	intake.DeferredAuditClaim,
) error {
	return nil
}

type orderedDeferredRecorder struct {
	mu            sync.Mutex
	order         *[]string
	records       []evaluation.Record
	err           error
	commitStarted chan struct{}
	releaseCommit chan struct{}
	panicOnCommit bool
	store         *deferredLedgerStore
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
	auditEntries []audit.NormalizedEntry,
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
	if recorder.store != nil {
		recorder.store.auditEntries = make([]intake.DeferredAuditEntry, 0, len(auditEntries))
		for index, entry := range auditEntries {
			recorder.store.auditEntries = append(recorder.store.auditEntries, intake.DeferredAuditEntry{
				Index: index, Entry: entry,
			})
		}
	}
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
	normalized    int
	calls         int
	failAt        int
	delivered     []string
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

func (sink *orderedDurableAuditSink) Normalize(
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	_ audit.Attrs,
) audit.NormalizedEntry {
	sink.normalized++
	return audit.NormalizedEntry{
		Event: audit.Event{
			EventID: "evt_test_" + msg, SchemaVersion: 1, Time: "2026-07-11T04:00:00Z",
			Level: level, Message: msg, System: system, SessionID: sessionID,
			EventName: eventName,
		},
		Fingerprint: "fingerprint_test_" + msg,
	}
}

func (sink *orderedDurableAuditSink) LogNormalizedDurable(
	ctx context.Context,
	entry audit.NormalizedEntry,
) error {
	sink.calls++
	*sink.order = append(*sink.order, "audit")
	if sink.contextErrors != nil {
		sink.contextErrors <- ctx.Err()
	}
	if sink.failAt > 0 && sink.calls == sink.failAt {
		return errors.New("forced normalized audit failure")
	}
	sink.delivered = append(sink.delivered, entry.Event.EventID)
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

func TestDeferredAuditRetryDoesNotReevaluate(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(48, 0)
	store := &deferredLedgerStore{record: record, order: &order, replayRecord: record}
	recorder := &orderedDeferredRecorder{order: &order, records: nil}
	sink := &orderedDurableAuditSink{
		order: &order, err: errors.New("audit unavailable"), delivered: make([]string, 0),
	}
	processor := deferredLedgerProcessor(t, store, recorder, sink)
	hotEvent := deferredLedgerHotEvent(t, processor.cfg, record)

	processor.processRecord(
		context.Background(), context.Background(), record,
		deferredLedgerClaim(record), &hotEvent, nil,
	)
	if len(recorder.records) != 1 || len(store.auditEntries) == 0 {
		t.Fatalf("records/outbox = %d/%d, want one evaluation and pending audit", len(recorder.records), len(store.auditEntries))
	}
	sink.err = nil
	if err := processor.ReplayPendingAudit(context.Background()); err != nil {
		t.Fatalf("ReplayPendingAudit: %v", err)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("evaluation records = %d, want no reevaluation", len(recorder.records))
	}
	if len(store.auditEntries) != 0 {
		t.Fatalf("pending audit entries = %d, want complete", len(store.auditEntries))
	}
}

func TestDeferredAuditPartialRetryResumesUndeliveredEntry(t *testing.T) {
	order := make([]string, 0)
	record := deferredLedgerRecord(49, 0)
	store := &deferredLedgerStore{record: record, order: &order, replayRecord: record}
	recorder := &orderedDeferredRecorder{order: &order, records: nil}
	sink := &orderedDurableAuditSink{
		order: &order, failAt: 2, delivered: make([]string, 0),
	}
	processor := deferredLedgerProcessor(t, store, recorder, sink)
	hotEvent := deferredLedgerHotEvent(t, processor.cfg, record)

	processor.processRecord(
		context.Background(), context.Background(), record,
		deferredLedgerClaim(record), &hotEvent, nil,
	)
	if len(store.auditEntries) != 1 || len(sink.delivered) != 1 {
		t.Fatalf("pending/delivered = %d/%d, want 1/1", len(store.auditEntries), len(sink.delivered))
	}
	sink.failAt = 0
	if err := processor.ReplayPendingAudit(context.Background()); err != nil {
		t.Fatalf("ReplayPendingAudit: %v", err)
	}
	if len(recorder.records) != 1 || len(sink.delivered) != 2 || sink.calls != 3 {
		t.Fatalf(
			"records/delivered/calls = %d/%d/%d, want 1/2/3",
			len(recorder.records), len(sink.delivered), sink.calls,
		)
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
	if deferredStore, ok := store.(*deferredLedgerStore); ok {
		if orderedRecorder, ok := recorder.(*orderedDeferredRecorder); ok {
			orderedRecorder.store = deferredStore
		}
	}
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
