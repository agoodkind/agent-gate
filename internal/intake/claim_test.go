package intake

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestExpiredDeferredClaimCanBeReclaimed(t *testing.T) {
	store, err := OpenSQLite(
		context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	originalNow := intakeNow
	t.Cleanup(func() { intakeNow = originalNow })
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	intakeNow = func() time.Time { return now }
	receipt, err := store.Append(context.Background(), Record{
		EventID: "event-expiring-claim", System: "codex", SessionID: "session",
		EventName: "PreToolUse", RawPayload: []byte(`{}`),
		NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, first, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner-a", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("first ClaimDeferred: %v", err)
	}
	now = now.Add(5 * time.Second)
	if _, _, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner-b", 10*time.Second,
	); !errors.Is(err, ErrDeferredClaimUnavailable) {
		t.Fatalf("live claim error = %v, want unavailable", err)
	}
	now = now.Add(6 * time.Second)
	_, second, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner-b", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("expired ClaimDeferred: %v", err)
	}
	if first.Attempt != 1 || second.Attempt != 2 {
		t.Fatalf("claim attempts = %d, %d", first.Attempt, second.Attempt)
	}
}

func TestRenewedDeferredClaimCannotBeReclaimedAfterOriginalExpiry(t *testing.T) {
	store, err := OpenSQLite(
		context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	originalNow := intakeNow
	t.Cleanup(func() { intakeNow = originalNow })
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	intakeNow = func() time.Time { return now }
	receipt, err := store.Append(context.Background(), Record{
		EventID: "event-renewed-claim", System: "codex", SessionID: "session",
		EventName: "PreToolUse", RawPayload: []byte(`{}`),
		NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.MarkDeferredPending(
		context.Background(), receipt.EventID, receipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	_, claim, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner-a", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	now = now.Add(8 * time.Second)
	if err := store.RenewDeferredClaim(
		context.Background(), claim, 10*time.Second,
	); err != nil {
		t.Fatalf("RenewDeferredClaim: %v", err)
	}
	now = now.Add(3 * time.Second)
	if _, _, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "owner-b", 10*time.Second,
	); !errors.Is(err, ErrDeferredClaimUnavailable) {
		t.Fatalf("renewed claim error = %v, want unavailable", err)
	}
}
