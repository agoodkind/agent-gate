package intake_test

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/intake"
)

func TestPendingMetadataFrozenPagesAndLaterEligibility(t *testing.T) {
	store := openDetailStore(t, filepath.Join(t.TempDir(), "audit.db"), fullDetailPolicy())
	var receipts []intake.AppendResult
	for index := range 207 {
		receipt := appendAtomicRecord(t, store, fmt.Sprintf("metadata-%d", index))
		receipts = append(receipts, receipt)
		if index == 0 {
			continue
		}
		if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, atomicEvaluationRecord(receipt, fmt.Sprintf("hot-%d", index), "hot", 1), nil); err != nil {
			t.Fatal(err)
		}
	}
	_, claim, err := store.ClaimDeferred(t.Context(), receipts[1].ReceiptID, "future", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := store.ScheduleDeferredRetry(t.Context(), claim, future); err != nil {
		t.Fatal(err)
	}
	_, live, err := store.ClaimDeferred(t.Context(), receipts[2].ReceiptID, "live", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if live.ReceiptID != receipts[2].ReceiptID {
		t.Fatal("claim changed receipt")
	}
	cursor, err := store.BeginPending(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.PendingPage(t.Context(), cursor)
	if err != nil || len(first) != 100 {
		t.Fatalf("first page = %d: %v", len(first), err)
	}
	if first[0].ClaimOwner != "live" || first[0].ClaimExpiresAt == nil {
		t.Fatalf("live claim metadata = %+v", first[0])
	}
	for _, entry := range first {
		cursor.Advance(entry)
	}
	lower := receipts[0]
	if err := store.CommitHotEvaluation(t.Context(), lower.EventID, lower.ReceiptID, true, atomicEvaluationRecord(lower, "late-hot", "hot", 1), nil); err != nil {
		t.Fatal(err)
	}
	second, err := store.PendingPage(t.Context(), cursor)
	if err != nil || len(second) != 100 || second[0].ReceiptID <= first[len(first)-1].ReceiptID {
		t.Fatalf("second page = %d: %v", len(second), err)
	}
	for _, entry := range second {
		cursor.Advance(entry)
	}
	last, err := store.PendingPage(t.Context(), cursor)
	if err != nil || len(last) != 5 {
		t.Fatalf("last page = %d: %v", len(last), err)
	}
	next, err := store.FirstFuturePending(t.Context(), cursor.EligibilityTime)
	if err != nil || next == nil || next.ReceiptID != receipts[1].ReceiptID || !next.NextAttemptAt.Equal(future) {
		t.Fatalf("future = %+v: %v", next, err)
	}
	revisit, err := store.BeginPending(t.Context(), future.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	latePage, err := store.PendingPage(t.Context(), revisit)
	if err != nil || latePage[0].ReceiptID != lower.ReceiptID {
		t.Fatalf("late receipt did not reappear: %+v, %v", latePage, err)
	}
	revisit.RetryTimes = true
	duePage, err := store.PendingPage(t.Context(), revisit)
	if err != nil || len(duePage) != 1 || duePage[0].ReceiptID != receipts[1].ReceiptID {
		t.Fatalf("due retry = %+v: %v", duePage, err)
	}
}

func TestPendingRetryPagesAdvancePastNewReceipts(t *testing.T) {
	store := openDetailStore(t, filepath.Join(t.TempDir(), "audit.db"), fullDetailPolicy())
	at := time.Now().Add(time.Minute)
	cursor, err := store.BeginPending(t.Context(), at)
	if err != nil {
		t.Fatal(err)
	}
	cursor.RetryTimes = true
	for index := range 103 {
		receipt := appendAtomicRecord(t, store, fmt.Sprintf("new-%d", index))
		if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, atomicEvaluationRecord(receipt, fmt.Sprintf("hot-new-%d", index), "hot", 1), nil); err != nil {
			t.Fatal(err)
		}
		_, claim, err := store.ClaimDeferred(t.Context(), receipt.ReceiptID, "schedule", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ScheduleDeferredRetry(t.Context(), claim, at.Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.PendingPage(t.Context(), cursor)
	if err != nil || len(entries) != 100 {
		t.Fatalf("raw page = %d: %v", len(entries), err)
	}
	for _, entry := range entries {
		if entry.ReceiptID <= cursor.MaximumReceiptID {
			t.Fatal("receipt ceiling changed")
		}
		cursor.Advance(entry)
	}
	entries, err = store.PendingPage(t.Context(), cursor)
	if err != nil || len(entries) != 3 {
		t.Fatalf("remaining raw page = %d: %v", len(entries), err)
	}
}
