package intake_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestCommitHotEvaluationRollsBackPendingWhenLedgerInsertFails(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-hot-rollback")
	if _, err := store.Handle().Exec(`
		create trigger fail_hot_evaluation before insert on gate_evaluations
		begin select raise(abort, 'forced evaluation failure'); end
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	err := store.CommitHotEvaluation(
		context.Background(), receipt.EventID, receipt.ReceiptID, true,
		atomicEvaluationRecord(receipt, "eval-hot-rollback", "hot", 1),
	)
	if err == nil {
		t.Fatal("CommitHotEvaluation succeeded with failing evaluation trigger")
	}
	pending, listErr := store.ListDeferredPending(context.Background(), 0)
	if listErr != nil {
		t.Fatalf("ListDeferredPending: %v", listErr)
	}
	if len(pending) != 0 {
		t.Fatalf("pending records = %+v, want rollback", pending)
	}
}

func TestCommitHotEvaluationStoresPendingAndLedgerTogether(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-hot-commit")
	record := atomicEvaluationRecord(receipt, "eval-hot-commit", "hot", 1)

	if err := store.CommitHotEvaluation(
		context.Background(), receipt.EventID, receipt.ReceiptID, true, record,
	); err != nil {
		t.Fatalf("CommitHotEvaluation: %v", err)
	}
	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ReceiptID != receipt.ReceiptID {
		t.Fatalf("pending records = %+v", pending)
	}
	if _, err := store.Evaluations().Get(context.Background(), record.Evaluation.EvaluationID); err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
}

func TestClaimDeferredAllowsOneConcurrentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	firstStore := openAtomicStore(t, path)
	secondStore := openAtomicStore(t, path)
	receipt := appendAtomicRecord(t, firstStore, "event-claim")
	if err := firstStore.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}

	owners := []string{"owner-a", "owner-b"}
	stores := []*intake.Store{firstStore, secondStore}
	claims := make(chan intake.DeferredClaim, 2)
	errorResults := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for i := range owners {
		waitGroup.Go(func() {
			_, claim, err := stores[i].ClaimDeferred(
				context.Background(), receipt.ReceiptID, owners[i], 30*time.Second,
			)
			claims <- claim
			errorResults <- err
		})
	}
	waitGroup.Wait()
	close(claims)
	close(errorResults)

	claimCount := 0
	for claim := range claims {
		if claim.Owner != "" {
			claimCount++
		}
	}
	lostCount := 0
	for err := range errorResults {
		if errors.Is(err, intake.ErrDeferredClaimUnavailable) {
			lostCount++
			continue
		}
		if err != nil {
			t.Fatalf("ClaimDeferred: %v", err)
		}
	}
	if claimCount != 1 || lostCount != 1 {
		t.Fatalf("claims = %d, unavailable = %d", claimCount, lostCount)
	}
}

func TestCommitDeferredEvaluationRejectsStaleClaim(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-stale-claim")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "current-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	stale := claim
	stale.Owner = "stale-owner"
	record := atomicEvaluationRecord(receipt, "eval-stale", "deferred", claim.Attempt)

	err = store.CommitDeferredEvaluation(context.Background(), stale, record, nil)
	if !errors.Is(err, intake.ErrDeferredClaimLost) {
		t.Fatalf("CommitDeferredEvaluation error = %v, want ErrDeferredClaimLost", err)
	}
	if _, err := store.Evaluations().Get(context.Background(), record.Evaluation.EvaluationID); !errors.Is(err, evaluation.ErrNotFound) {
		t.Fatalf("Get evaluation error = %v, want not found", err)
	}
}

func TestCommitDeferredEvaluationStoresLedgerAndCompletionTogether(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-deferred-commit")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	record := atomicEvaluationRecord(receipt, "eval-deferred-commit", "deferred", claim.Attempt)

	if err := store.CommitDeferredEvaluation(context.Background(), claim, record, nil); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending records = %+v, want complete", pending)
	}
	if _, err := store.Evaluations().Get(context.Background(), record.Evaluation.EvaluationID); err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
}

func TestCommitDeferredEvaluationReopensWithExactPendingAudit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openAtomicStore(t, path)
	receipt := appendAtomicRecord(t, store, "event-deferred-outbox")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "evaluation-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	record := atomicEvaluationRecord(
		receipt, "eval-deferred-outbox", "deferred", claim.Attempt,
	)
	entries := atomicAuditEntries(receipt.EventID)
	if err := store.CommitDeferredEvaluation(
		context.Background(), claim, record, entries,
	); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close before reopen: %v", err)
	}

	reopened := openAtomicStore(t, path)
	pending, err := reopened.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending evaluations = %+v, want none", pending)
	}
	receiptIDs, err := reopened.ListPendingDeferredAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	if len(receiptIDs) != 1 || receiptIDs[0] != receipt.ReceiptID {
		t.Fatalf("pending audit receipts = %+v, want [%d]", receiptIDs, receipt.ReceiptID)
	}
	gotEntries, auditClaim, err := reopened.ClaimDeferredAudit(
		context.Background(), receipt.ReceiptID, "audit-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit: %v", err)
	}
	if auditClaim.Attempt != 1 {
		t.Fatalf("audit claim attempt = %d, want 1", auditClaim.Attempt)
	}
	if len(gotEntries) != len(entries) {
		t.Fatalf("audit entries = %d, want %d", len(gotEntries), len(entries))
	}
	for i := range entries {
		if gotEntries[i].Index != i ||
			gotEntries[i].Entry.Event.EventID != entries[i].Event.EventID ||
			gotEntries[i].Entry.Event.Time != entries[i].Event.Time ||
			gotEntries[i].Entry.Fingerprint != entries[i].Fingerprint {
			t.Fatalf("audit entry %d = %+v, want %+v", i, gotEntries[i], entries[i])
		}
	}
	if _, err := reopened.Evaluations().Get(
		context.Background(), record.Evaluation.EvaluationID,
	); err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
}

func TestCommitDeferredEvaluationRollsBackWhenOutboxInsertFails(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-outbox-rollback")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	if _, err := store.Handle().Exec(`
		create trigger fail_deferred_audit_entry
		before insert on deferred_audit_outbox_entries
		begin select raise(abort, 'forced outbox failure'); end
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	record := atomicEvaluationRecord(receipt, "eval-outbox-rollback", "deferred", claim.Attempt)
	err = store.CommitDeferredEvaluation(
		context.Background(), claim, record, atomicAuditEntries(receipt.EventID),
	)
	if err == nil {
		t.Fatal("CommitDeferredEvaluation succeeded with failing outbox trigger")
	}
	if _, err := store.Evaluations().Get(
		context.Background(), record.Evaluation.EvaluationID,
	); !errors.Is(err, evaluation.ErrNotFound) {
		t.Fatalf("Get evaluation error = %v, want not found", err)
	}
	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ReceiptID != receipt.ReceiptID {
		t.Fatalf("pending evaluations = %+v, want receipt %d", pending, receipt.ReceiptID)
	}
	outbox, err := store.ListPendingDeferredAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	if len(outbox) != 0 {
		t.Fatalf("pending outbox = %+v, want rollback", outbox)
	}
}

func TestDeferredAuditClaimResumesOnlyUndeliveredEntries(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "event-outbox-partial")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, evaluationClaim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "evaluation-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	record := atomicEvaluationRecord(
		receipt, "eval-outbox-partial", "deferred", evaluationClaim.Attempt,
	)
	if err := store.CommitDeferredEvaluation(
		context.Background(), evaluationClaim, record, atomicAuditEntries(receipt.EventID),
	); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	entries, firstClaim, err := store.ClaimDeferredAudit(
		context.Background(), receipt.ReceiptID, "audit-owner-a", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit first: %v", err)
	}
	if err := store.MarkDeferredAuditEntryDelivered(
		context.Background(), firstClaim, entries[0].Index,
	); err != nil {
		t.Fatalf("MarkDeferredAuditEntryDelivered: %v", err)
	}
	if err := store.ReleaseDeferredAuditClaim(context.Background(), firstClaim); err != nil {
		t.Fatalf("ReleaseDeferredAuditClaim: %v", err)
	}
	remaining, secondClaim, err := store.ClaimDeferredAudit(
		context.Background(), receipt.ReceiptID, "audit-owner-b", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit second: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Index != 1 {
		t.Fatalf("remaining entries = %+v, want index 1", remaining)
	}
	if err := store.MarkDeferredAuditEntryDelivered(
		context.Background(), firstClaim, remaining[0].Index,
	); !errors.Is(err, intake.ErrDeferredAuditClaimLost) {
		t.Fatalf("stale claim error = %v, want ErrDeferredAuditClaimLost", err)
	}
	if err := store.MarkDeferredAuditEntryDelivered(
		context.Background(), secondClaim, remaining[0].Index,
	); err != nil {
		t.Fatalf("MarkDeferredAuditEntryDelivered second: %v", err)
	}
	if err := store.CompleteDeferredAudit(context.Background(), secondClaim); err != nil {
		t.Fatalf("CompleteDeferredAudit: %v", err)
	}
	pending, err := store.ListPendingDeferredAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending outbox = %+v, want complete", pending)
	}
}

func appendAtomicRecord(
	t *testing.T,
	store *intake.Store,
	eventID string,
) intake.AppendResult {
	t.Helper()
	receipt, err := store.Append(context.Background(), intake.Record{
		EventID: eventID, System: "codex", SessionID: "session",
		EventName: "PreToolUse", RawPayload: []byte(`{}`),
		NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return receipt
}

func openAtomicStore(t *testing.T, path string) *intake.Store {
	t.Helper()
	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return store
}

func atomicEvaluationRecord(
	receipt intake.AppendResult,
	evaluationID string,
	mode string,
	attempt int,
) evaluation.Record {
	now := time.Date(2026, 7, 11, 4, 0, 0, 0, time.UTC)
	return evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID: evaluationID, ReceiptID: receipt.ReceiptID, EventID: receipt.EventID,
			Attempt: attempt, Mode: mode, ConfigHash: "sha256:config",
			EngineVersion: "version", EngineCommit: "commit", EngineBuildHash: "build",
			InputHash: "sha256:input", StartedAt: now, CompletedAt: now,
			FinalVerdict: "allow", FinalSource: "deterministic",
			EnforcementAction: "allow", ErrorJSON: json.RawMessage(`{}`),
		},
		Layers: make([]evaluation.Layer, 0), Labels: make([]evaluation.Label, 0),
	}
}

func atomicAuditEntries(eventID string) []audit.NormalizedEntry {
	return []audit.NormalizedEntry{
		{
			Event: audit.Event{
				EventID: "evt_received", SchemaVersion: 1,
				Time: "2026-07-11T04:00:00Z", Level: "info", Message: "hook.received",
				System: "codex", SessionID: "session", EventName: "PreToolUse",
			},
			Fingerprint: "fingerprint-received-" + eventID,
		},
		{
			Event: audit.Event{
				EventID: "evt_blocked", SchemaVersion: 1,
				Time: "2026-07-11T04:00:01Z", Level: "info", Message: "hook.blocked",
				System: "codex", SessionID: "session", EventName: "PreToolUse",
			},
			Fingerprint: "fingerprint-blocked-" + eventID,
		},
	}
}
