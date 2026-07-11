package intake_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/intake"
)

func TestDeferredAuditClaimAllowsOneConcurrentOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	firstStore := openAtomicStore(t, path)
	receipt := appendAtomicRecord(t, firstStore, "event-audit-claim")
	if err := firstStore.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, evaluationClaim, err := firstStore.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "evaluation-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	record := atomicEvaluationRecord(
		receipt, "eval-audit-claim", "deferred", evaluationClaim.Attempt,
	)
	if err := firstStore.CommitDeferredEvaluation(
		context.Background(), evaluationClaim, record, atomicAuditEntries(receipt.EventID),
	); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	secondStore := openAtomicStore(t, path)

	stores := []*intake.Store{firstStore, secondStore}
	owners := []string{"audit-owner-a", "audit-owner-b"}
	claims := make(chan intake.DeferredAuditClaim, 2)
	errorsFound := make(chan error, 2)
	var waitGroup sync.WaitGroup
	for i := range stores {
		waitGroup.Go(func() {
			_, claim, err := stores[i].ClaimDeferredAudit(
				context.Background(), receipt.ReceiptID, owners[i], 30*time.Second,
			)
			claims <- claim
			errorsFound <- err
		})
	}
	waitGroup.Wait()
	close(claims)
	close(errorsFound)
	claimCount := 0
	for claim := range claims {
		if claim.Owner != "" {
			claimCount++
		}
	}
	unavailableCount := 0
	for err := range errorsFound {
		if errors.Is(err, intake.ErrDeferredAuditClaimUnavailable) {
			unavailableCount++
			continue
		}
		if err != nil {
			t.Fatalf("ClaimDeferredAudit: %v", err)
		}
	}
	if claimCount != 1 || unavailableCount != 1 {
		t.Fatalf("claims/unavailable = %d/%d, want 1/1", claimCount, unavailableCount)
	}
}

func TestDeferredAuditOutboxMigrationDoesNotInventHistoricalRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openAtomicStore(t, path)
	receipt := appendAtomicRecord(t, store, "event-historical-complete")
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	if err := store.MarkDeferredComplete(context.Background(), receipt.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredComplete: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := database.Exec(`drop table deferred_audit_outbox_entries`); err != nil {
		t.Fatalf("drop outbox entries: %v", err)
	}
	if _, err := database.Exec(`drop table deferred_audit_outbox`); err != nil {
		t.Fatalf("drop outbox: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	reopened := openAtomicStore(t, path)
	pending, err := reopened.ListPendingDeferredAudit(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("invented historical outbox rows = %+v", pending)
	}
	historical, err := reopened.GetReceipt(context.Background(), receipt.ReceiptID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if historical.DeferredState != intake.DeferredStateComplete {
		t.Fatalf("historical state = %q, want complete", historical.DeferredState)
	}
}
