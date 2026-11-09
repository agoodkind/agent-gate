package intake_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/audit"
)

func TestCanceledTransactionWritesReturnErrorsWithoutRecoveryWarnings(t *testing.T) {
	root := t.TempDir()
	store := openDetailStore(t, filepath.Join(root, "audit.db"), fullDetailPolicy())
	receipt := appendAtomicRecord(t, store, "canceled-transaction")
	logPath := filepath.Join(root, "warnings.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logFile.Close() }()
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))
	defer slog.SetDefault(original)
	transaction, err := store.Handle().BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = transaction.Rollback() }()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := audit.WriteEventsInTx(
		ctx,
		transaction,
		[]audit.Event{{EventID: "canceled-audit", SchemaVersion: 1}},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("audit transaction cancellation = %v", err)
	}

	record := atomicEvaluationRecord(receipt, "canceled-evaluation", "deferred", 1)
	if err := store.Evaluations().RecordCompletedInTx(ctx, transaction, record); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("evaluation transaction cancellation = %v", err)
	}

	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}

	if body, err := os.ReadFile(logPath); err != nil || len(body) != 0 {
		t.Fatalf("expected cancellation emitted warnings: %q: %v", body, err)
	}

	var evaluations, events int
	if err := store.Handle().QueryRowContext(
		t.Context(),
		readIntakeSQLFixture(t, "canceled_transaction_counts.sql"),
	).Scan(
		&evaluations,
		&events,
	); err != nil || evaluations != 0 ||
		events != 0 {
		t.Fatalf("canceled writes survived: evaluations=%d events=%d: %v", evaluations, events, err)
	}

	if _, err := store.GetReceipt(t.Context(), receipt.ReceiptID); err != nil {
		t.Fatalf("durable receipt did not survive: %v", err)
	}
}
