package intake_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

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

	err = store.CommitDeferredEvaluation(context.Background(), stale, record)
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

	if err := store.CommitDeferredEvaluation(context.Background(), claim, record); err != nil {
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
