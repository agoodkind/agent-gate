package intake_test

import (
	"errors"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
)

func TestHotAuditFailureRollsBackEvaluationAndPending(t *testing.T) {
	for _, policy := range []config.AuditStoragePolicy{fullDetailPolicy(), minimalDetailPolicy()} {
		t.Run(string(policy.Profile), func(t *testing.T) {
			store := openDetailStore(t, filepath.Join(t.TempDir(), "audit.db"), policy)
			receipt := appendAtomicRecord(t, store, "hot-rollback")
			if _, err := store.Handle().ExecContext(t.Context(), readIntakeSQLFixture(t, "fail_hot_audit_insert.sql")); err != nil {
				t.Fatal(err)
			}
			record := atomicEvaluationRecord(receipt, "hot-evaluation", "hot", 1)
			record.Layers = []evaluation.Layer{atomicEvaluationLayer()}
			err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, record, atomicAuditEntries(receipt.EventID))
			if err == nil {
				t.Fatal("expected hot audit insertion failure")
			}
			if _, err := store.Evaluations().Get(t.Context(), record.Evaluation.EvaluationID); !errors.Is(err, evaluation.ErrNotFound) {
				t.Fatalf("hot evaluation survived rollback: %v", err)
			}
			pending, err := store.ListDeferredPending(t.Context(), 10)
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending = %v, error = %v", pending, err)
			}
			assertAtomicAuditCount(t, store, 0)
			var layers int
			if err := store.Handle().QueryRowContext(t.Context(), readIntakeSQLFixture(t, "count_evaluation_layers.sql")).Scan(&layers); err != nil || layers != 0 {
				t.Fatalf("layers = %d, error = %v", layers, err)
			}
		})
	}
}

func TestHotAuditReopensAndRejectsRepeatWithoutDuplicateRows(t *testing.T) {
	for _, policy := range []config.AuditStoragePolicy{fullDetailPolicy(), minimalDetailPolicy()} {
		t.Run(string(policy.Profile), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.db")
			store := openDetailStore(t, path, policy)
			receipt := appendAtomicRecord(t, store, "hot-success")
			record := atomicEvaluationRecord(receipt, "hot-evaluation", "hot", 1)
			if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, record, atomicAuditEntries(receipt.EventID)); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitHotEvaluation(t.Context(), receipt.EventID, receipt.ReceiptID, true, record, atomicAuditEntries(receipt.EventID)); err == nil {
				t.Fatal("expected duplicate hot evaluation rejection")
			}
			if err := store.Handle().Close(); err != nil {
				t.Fatal(err)
			}
			store = openDetailStore(t, path, policy)
			var evaluations int
			if err := store.Handle().QueryRowContext(t.Context(), readIntakeSQLFixture(t, "count_hot_evaluations.sql")).Scan(&evaluations); err != nil || evaluations != 1 {
				t.Fatalf("hot evaluations = %d, error = %v", evaluations, err)
			}
			assertAtomicAuditCount(t, store, 2)
			pending, err := store.ListDeferredPending(t.Context(), 10)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending = %v, error = %v", pending, err)
			}
		})
	}
}
