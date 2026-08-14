package intake_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/intake"
)

func TestPendingDeferredAuditAlwaysRetainsPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, minimalDetailPolicy())
	receipt := createPendingDeferredAudit(t, store, "event-audit-payload-live")

	entries, firstClaim, err := store.ClaimDeferredAudit(
		t.Context(), receipt.ReceiptID, "audit-owner-first", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit first: %v", err)
	}
	if err := store.MarkDeferredAuditEntryDelivered(
		t.Context(), firstClaim, entries[0].Index,
	); err != nil {
		t.Fatalf("MarkDeferredAuditEntryDelivered: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `
		update deferred_audit_outbox
		set claim_expires_at = '2000-01-01T00:00:00Z'
		where receipt_id = ?
	`, receipt.ReceiptID); err != nil {
		t.Fatalf("expire deferred audit claim: %v", err)
	}
	assertDeferredAuditPayloadRows(t, store.Handle(), receipt.ReceiptID, 2)
	if err := store.Close(); err != nil {
		t.Fatalf("Close before retry: %v", err)
	}

	reopened := openDetailStore(t, path, minimalDetailPolicy())
	remaining, secondClaim, err := reopened.ClaimDeferredAudit(
		t.Context(), receipt.ReceiptID, "audit-owner-second", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit after restart: %v", err)
	}
	if len(remaining) != 1 || remaining[0].Index != 1 || secondClaim.Attempt != 2 {
		t.Fatalf("retry entries/attempt = %+v/%d, want entry 1 attempt 2", remaining, secondClaim.Attempt)
	}
	assertDeferredAuditPayloadRows(t, reopened.Handle(), receipt.ReceiptID, 2)
}

func TestCompletedDeferredAuditCanDemotePayload(t *testing.T) {
	store := openDetailStore(
		t,
		filepath.Join(t.TempDir(), "audit.db"),
		minimalDetailPolicy(),
	)
	receipt := createPendingDeferredAudit(t, store, "event-audit-payload-complete")

	entries, claim, err := store.ClaimDeferredAudit(
		t.Context(), receipt.ReceiptID, "audit-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit: %v", err)
	}
	for _, entry := range entries {
		if err := store.MarkDeferredAuditEntryDelivered(
			t.Context(), claim, entry.Index,
		); err != nil {
			t.Fatalf("MarkDeferredAuditEntryDelivered %d: %v", entry.Index, err)
		}
	}
	if err := store.CompleteDeferredAudit(t.Context(), claim); err != nil {
		t.Fatalf("CompleteDeferredAudit: %v", err)
	}

	assertDeferredAuditPayloadRows(t, store.Handle(), receipt.ReceiptID, 0)
	var recorded int
	var available int
	if err := store.Handle().QueryRowContext(t.Context(), `
		select sum(payload_recorded), sum(payload_available)
		from deferred_audit_outbox_entries where receipt_id = ?
	`, receipt.ReceiptID).Scan(&recorded, &available); err != nil {
		t.Fatalf("read deferred audit payload state: %v", err)
	}
	if recorded != 0 || available != 0 {
		t.Fatalf("payload recorded/available = %d/%d, want 0/0", recorded, available)
	}
	assertDisabledGraphDetailDemoted(t, store.Handle(), receipt.EventID, 2)
}

func TestCompletedDeferredAuditRetainsEnabledPayload(t *testing.T) {
	store := openAtomicStore(t, filepath.Join(t.TempDir(), "audit.db"))
	receipt := createPendingDeferredAudit(t, store, "event-audit-payload-retained")

	entries, claim, err := store.ClaimDeferredAudit(
		t.Context(), receipt.ReceiptID, "audit-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferredAudit: %v", err)
	}
	for _, entry := range entries {
		if err := store.MarkDeferredAuditEntryDelivered(
			t.Context(), claim, entry.Index,
		); err != nil {
			t.Fatalf("MarkDeferredAuditEntryDelivered %d: %v", entry.Index, err)
		}
	}
	if err := store.CompleteDeferredAudit(t.Context(), claim); err != nil {
		t.Fatalf("CompleteDeferredAudit: %v", err)
	}

	assertDeferredAuditPayloadRows(t, store.Handle(), receipt.ReceiptID, 2)
	var recorded int
	var available int
	if err := store.Handle().QueryRowContext(t.Context(), `
		select sum(payload_recorded), sum(payload_available)
		from deferred_audit_outbox_entries where receipt_id = ?
	`, receipt.ReceiptID).Scan(&recorded, &available); err != nil {
		t.Fatalf("read retained deferred audit payload state: %v", err)
	}
	if recorded != 2 || available != 2 {
		t.Fatalf("retained payload recorded/available = %d/%d, want 2/2", recorded, available)
	}
}

func TestClaimDeferredAuditRejectsMissingLivePayload(t *testing.T) {
	store := openDetailStore(
		t,
		filepath.Join(t.TempDir(), "audit.db"),
		minimalDetailPolicy(),
	)
	receipt := createPendingDeferredAudit(t, store, "event-audit-payload-missing")
	if _, err := store.Handle().ExecContext(t.Context(), `
		delete from deferred_audit_outbox_entry_details
		where receipt_id = ? and entry_index = 0
	`, receipt.ReceiptID); err != nil {
		t.Fatalf("remove live payload detail: %v", err)
	}

	_, _, err := store.ClaimDeferredAudit(
		t.Context(), receipt.ReceiptID, "audit-owner", 30*time.Second,
	)
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("ClaimDeferredAudit error = %v, want integrity error", err)
	}
	var claimOwner sql.NullString
	var attempt int
	var state string
	if err := store.Handle().QueryRowContext(t.Context(), `
		select claim_owner, claim_attempt, state
		from deferred_audit_outbox where receipt_id = ?
	`, receipt.ReceiptID).Scan(&claimOwner, &attempt, &state); err != nil {
		t.Fatalf("read outbox after rejected claim: %v", err)
	}
	if claimOwner.Valid || attempt != 0 || state != "pending" {
		t.Fatalf(
			"outbox after rejected claim = owner %q valid %t attempt %d state %q",
			claimOwner.String,
			claimOwner.Valid,
			attempt,
			state,
		)
	}
}

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
	t.Skip("database backward compatibility was removed")
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
	if _, err := database.Exec(`drop table audit_schema_migrations`); err != nil {
		t.Fatalf("remove schema version from legacy database: %v", err)
	}
	if _, err := database.Exec(`pragma user_version = 0`); err != nil {
		t.Fatalf("reset legacy user version: %v", err)
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

func createPendingDeferredAudit(
	t *testing.T,
	store *intake.Store,
	eventID string,
) intake.AppendResult {
	t.Helper()
	receipt := appendAtomicRecord(t, store, eventID)
	hotRecord := atomicEvaluationRecord(receipt, "hot-evaluation-"+eventID, "hot", 1)
	hotRecord.Layers = append(hotRecord.Layers, atomicEvaluationLayer())
	if err := store.CommitHotEvaluation(
		t.Context(), receipt.EventID, receipt.ReceiptID, true, hotRecord,
	); err != nil {
		t.Fatalf("CommitHotEvaluation: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		t.Context(), receipt.ReceiptID, "evaluation-owner", 30*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	record := atomicEvaluationRecord(
		receipt,
		"evaluation-"+eventID,
		"deferred",
		claim.Attempt,
	)
	record.Layers = append(record.Layers, atomicEvaluationLayer())
	if err := store.CommitDeferredEvaluation(
		t.Context(), claim, record, atomicAuditEntries(receipt.EventID),
	); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	return receipt
}

func assertDeferredAuditPayloadRows(
	t *testing.T,
	database *sql.DB,
	receiptID int64,
	want int,
) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), `
		select count(*) from deferred_audit_outbox_entry_details where receipt_id = ?
	`, receiptID).Scan(&count); err != nil {
		t.Fatalf("count deferred audit payload detail: %v", err)
	}
	if count != want {
		t.Fatalf("deferred audit payload rows = %d, want %d", count, want)
	}
}
