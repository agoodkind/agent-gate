package intake

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func TestBorrowedStoreKeepsCatalogDatabaseOpen(t *testing.T) {
	ctx := context.Background()
	database, err := auditstorage.OpenWriter(ctx, filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(ctx, database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, Record{RawPayload: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if err := database.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(ctx, Record{RawPayload: []byte(`{}`)}); err == nil {
		t.Fatal("borrower survived owner close")
	}
}

func TestDueDeferredPagesAndFencesRetries(t *testing.T) {
	ctx := context.Background()
	store, err := openFixtureIntake(t, ctx, filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Handle().Close() })
	original := intakeNow
	t.Cleanup(func() { intakeNow = original })
	now := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	intakeNow = func() time.Time { return now }
	var receipts []AppendResult
	for range 3 {
		receipt, err := store.Append(ctx, Record{EventID: "repeated", RawPayload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkDeferredPending(ctx, receipt.EventID, receipt.ReceiptID); err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	first, err := store.ListDueDeferred(ctx, now, 0, 2)
	if err != nil || len(first) != 2 || first[0] != receipts[0].ReceiptID || first[1] != receipts[1].ReceiptID {
		t.Fatalf("first page = %v, %v", first, err)
	}
	second, err := store.ListDueDeferred(ctx, now, first[1], 2)
	if err != nil || len(second) != 1 || second[0] != receipts[2].ReceiptID {
		t.Fatalf("second page = %v, %v", second, err)
	}
	_, claim, err := store.ClaimDeferred(ctx, first[0], "owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	due, err := store.ListDueDeferred(ctx, now, 0, 3)
	if err != nil || len(due) != 2 {
		t.Fatalf("live claim due = %v, %v", due, err)
	}
	if err := store.ScheduleDeferredRetry(ctx, claim, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := store.ScheduleDeferredRetry(ctx, claim, now); !errors.Is(err, ErrDeferredClaimLost) {
		t.Fatalf("stale retry = %v", err)
	}
	due, err = store.ListDueDeferred(ctx, now.Add(time.Second), 0, 3)
	if err != nil || len(due) != 2 {
		t.Fatalf("early retry = %v, %v", due, err)
	}
	now = now.Add(2 * time.Second)
	due, err = store.ListDueDeferred(ctx, now, 0, 3)
	if err != nil || len(due) != 3 {
		t.Fatalf("due retry = %v, %v", due, err)
	}
	_, next, err := store.ClaimDeferred(ctx, first[0], "next-owner", time.Minute)
	if err != nil || next.Attempt != 2 {
		t.Fatalf("next claim = %+v, %v", next, err)
	}
	if err := store.ScheduleDeferredRetry(ctx, claim, now); !errors.Is(err, ErrDeferredClaimLost) {
		t.Fatalf("reclaimed retry = %v", err)
	}
}
