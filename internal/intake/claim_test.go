package intake

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/evaluation"
)

func TestExpiredUnreclaimedDeferredClaimCanComplete(t *testing.T) {
	store, err := openFixtureIntake(
		t, context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Handle().Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	originalNow := intakeNow
	t.Cleanup(func() { intakeNow = originalNow })
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	intakeNow = func() time.Time { return now }
	receipt, err := store.Append(context.Background(), Record{
		EventID: "event-expired-unreclaimed", System: "codex", SessionID: "session",
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
		context.Background(), receipt.ReceiptID, "original-owner", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred: %v", err)
	}
	now = now.Add(11 * time.Second)
	record := claimEvaluationRecord(receipt, claim, "expired-unreclaimed")
	if err := store.CommitDeferredEvaluation(
		context.Background(), claim, record, nil,
	); err != nil {
		t.Fatalf("CommitDeferredEvaluation: %v", err)
	}
	if _, err := store.Evaluations().Get(
		context.Background(), record.Evaluation.EvaluationID,
	); err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
}

func TestReclaimedDeferredClaimRejectsOriginalCompletion(t *testing.T) {
	store, err := openFixtureIntake(
		t, context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Handle().Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	originalNow := intakeNow
	t.Cleanup(func() { intakeNow = originalNow })
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	intakeNow = func() time.Time { return now }
	receipt, err := store.Append(context.Background(), Record{
		EventID: "event-reclaimed-completion", System: "codex", SessionID: "session",
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
	_, original, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "original-owner", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred original: %v", err)
	}
	now = now.Add(11 * time.Second)
	_, replacement, err := store.ClaimDeferred(
		context.Background(), receipt.ReceiptID, "replacement-owner", 10*time.Second,
	)
	if err != nil {
		t.Fatalf("ClaimDeferred replacement: %v", err)
	}
	originalRecord := claimEvaluationRecord(receipt, original, "original")
	err = store.CommitDeferredEvaluation(
		context.Background(), original, originalRecord, nil,
	)
	if !errors.Is(err, ErrDeferredClaimLost) {
		t.Fatalf("original completion error = %v, want ErrDeferredClaimLost", err)
	}
	replacementRecord := claimEvaluationRecord(receipt, replacement, "replacement")
	if err := store.CommitDeferredEvaluation(
		context.Background(), replacement, replacementRecord, nil,
	); err != nil {
		t.Fatalf("replacement completion: %v", err)
	}
}

func TestExpiredDeferredClaimCanBeReclaimed(t *testing.T) {
	store, err := openFixtureIntake(t,
		context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Handle().Close(); err != nil {
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
	store, err := openFixtureIntake(t,
		context.Background(), filepath.Join(t.TempDir(), "audit.db"), nil,
	)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Handle().Close(); err != nil {
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

func claimEvaluationRecord(
	receipt AppendResult,
	claim DeferredClaim,
	evaluationID string,
) evaluation.Record {
	now := time.Date(2026, 7, 11, 4, 30, 0, 0, time.UTC)
	return evaluation.Record{Evaluation: evaluation.Evaluation{
		EvaluationID: evaluationID, ReceiptID: receipt.ReceiptID,
		EventID: receipt.EventID, Attempt: claim.Attempt, Mode: "deferred",
		ConfigHash: "config", EngineVersion: "version", EngineCommit: "commit",
		EngineBuildHash: "build", InputHash: "input", StartedAt: now,
		CompletedAt: now, FinalVerdict: "allow", FinalSource: "deterministic",
		EnforcementAction: "allow",
	}}
}
