package intake_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestDeferredAuditFailureRollsBackCompletion(t *testing.T) {
	store := newTestStore(t)
	receipt := appendAtomicRecord(t, store, "rollback")
	ctx := t.Context()
	if err := store.MarkDeferredPending(ctx, receipt.EventID, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
	_, claim, err := store.ClaimDeferred(ctx, receipt.ReceiptID, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/fail_audit_insert.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Handle().ExecContext(ctx, string(fixture)); err != nil {
		t.Fatal(err)
	}
	record := atomicEvaluationRecord(receipt, "evaluation", "deferred", claim.Attempt)
	err = store.CommitDeferredEvaluation(ctx, claim, record, atomicAuditEntries(receipt.EventID))
	if err == nil {
		t.Fatal("expected audit insertion failure")
	}
	if _, err := store.Evaluations().Get(ctx, "evaluation"); !errors.Is(err, evaluation.ErrNotFound) {
		t.Fatalf("evaluation survived rollback: %v", err)
	}
	pending, err := store.ListDeferredPending(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %v, error = %v", pending, err)
	}
	assertAtomicAuditCount(t, store, 0)
}

func TestDeferredCompletionReopensWithAuditEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openAtomicStore(t, path)
	receipt := appendAtomicRecord(t, store, "success")
	ctx := t.Context()
	if err := store.MarkDeferredPending(ctx, receipt.EventID, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
	_, claim, err := store.ClaimDeferred(ctx, receipt.ReceiptID, "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record := atomicEvaluationRecord(receipt, "evaluation", "deferred", claim.Attempt)
	if err := store.CommitDeferredEvaluation(ctx, claim, record, atomicAuditEntries(receipt.EventID)); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAtomicStore(t, path)
	if _, err := reopened.Evaluations().Get(ctx, "evaluation"); err != nil {
		t.Fatal(err)
	}
	assertAtomicAuditCount(t, reopened, 2)
	pending, err := reopened.ListDeferredPending(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending = %v, error = %v", pending, err)
	}
}

func assertAtomicAuditCount(t *testing.T, store *intake.Store, want int) {
	t.Helper()
	fixture, err := os.ReadFile("testdata/count_atomic_audit.sql")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.Handle().QueryRowContext(t.Context(), string(fixture)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("audit rows = %d, want %d", count, want)
	}
}
