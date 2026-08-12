package intake_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

type legacyAuditSummary struct {
	Receipt         intake.Record
	Evaluation      evaluation.Record
	PendingReceipts []int64
	AuditEvents     int
	Violations      int
	OutboxEntries   int
}

func TestOpenSQLiteMigrationRecordsLegacyAuditSchemaVersion(t *testing.T) {
	path := installLegacyAuditFixture(t)

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite migration: %v", err)
	}
	firstSummary := readLegacyAuditSummary(t, store)
	assertLegacySummaryFidelity(t, firstSummary)
	assertSchemaVersion(t, store.Handle(), 3)
	assertForeignKeysClean(t, store.Handle())
	firstAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), store.Handle(), 1)
	if err != nil {
		t.Fatalf("MigrationAppliedAt first open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close first store: %v", err)
	}

	reopened, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite reopen: %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close reopened store: %v", err)
		}
	})
	secondSummary := readLegacyAuditSummary(t, reopened)
	if !reflect.DeepEqual(secondSummary, firstSummary) {
		t.Fatalf("reopened summary changed\ngot:  %#v\nwant: %#v", secondSummary, firstSummary)
	}
	secondAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), reopened.Handle(), 1)
	if err != nil {
		t.Fatalf("MigrationAppliedAt reopen: %v", err)
	}
	if !secondAppliedAt.Equal(firstAppliedAt) {
		t.Fatalf("migration timestamp changed from %s to %s", firstAppliedAt, secondAppliedAt)
	}
}

func TestOpenSQLiteMigrationLeavesLegacyAutoVacuumModeUnchanged(t *testing.T) {
	path := installLegacyAuditFixture(t)
	database := openSQLiteHandle(t, path)
	assertAutoVacuumMode(t, database, 0)
	if err := database.Close(); err != nil {
		t.Fatalf("close fixture handle: %v", err)
	}

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite migration: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	assertAutoVacuumMode(t, store.Handle(), 0)
}

func installLegacyAuditFixture(t *testing.T) string {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "auditstorage", "testdata", "legacy_v1.sql"))
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	path := filepath.Join(t.TempDir(), "audit.db")
	database := openSQLiteHandle(t, path)
	if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatalf("install legacy fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy fixture: %v", err)
	}
	return path
}

func openSQLiteHandle(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite database: %v", err)
	}
	return database
}

func readLegacyAuditSummary(t *testing.T, store *intake.Store) legacyAuditSummary {
	t.Helper()
	receipt, err := store.GetReceipt(t.Context(), 1)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	evaluationRecord, err := store.Evaluations().Get(t.Context(), "eval-legacy")
	if err != nil {
		t.Fatalf("Get evaluation: %v", err)
	}
	pending, err := store.ListPendingDeferredAudit(t.Context(), 0)
	if err != nil {
		t.Fatalf("ListPendingDeferredAudit: %v", err)
	}
	return legacyAuditSummary{
		Receipt:         receipt,
		Evaluation:      evaluationRecord,
		PendingReceipts: pending,
		AuditEvents:     queryRowCount(t, store.Handle(), "events"),
		Violations:      queryRowCount(t, store.Handle(), "violations"),
		OutboxEntries:   queryRowCount(t, store.Handle(), "deferred_audit_outbox_entries"),
	}
}

func assertLegacySummaryFidelity(t *testing.T, summary legacyAuditSummary) {
	t.Helper()
	if summary.Receipt.EventID != "event-legacy" || summary.Receipt.DeferredState != intake.DeferredStatePending {
		t.Fatalf("legacy receipt = %#v", summary.Receipt)
	}
	if summary.Evaluation.Evaluation.EvaluationID != "eval-legacy" {
		t.Fatalf("legacy evaluation = %#v", summary.Evaluation.Evaluation)
	}
	if len(summary.Evaluation.Layers) != 1 || len(summary.Evaluation.Labels) != 1 {
		t.Fatalf("legacy evaluation children = %d layers, %d labels", len(summary.Evaluation.Layers), len(summary.Evaluation.Labels))
	}
	if !reflect.DeepEqual(summary.PendingReceipts, []int64{1}) {
		t.Fatalf("pending outbox receipts = %v, want [1]", summary.PendingReceipts)
	}
	if summary.AuditEvents != 1 || summary.Violations != 1 || summary.OutboxEntries != 1 {
		t.Fatalf("legacy row counts = events %d, violations %d, outbox entries %d", summary.AuditEvents, summary.Violations, summary.OutboxEntries)
	}
}

func queryRowCount(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(t.Context(), "select count(*) from "+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func assertSchemaVersion(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	got, err := auditstorage.SchemaVersion(t.Context(), database)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	var userVersion int
	if err := database.QueryRowContext(t.Context(), `pragma user_version`).Scan(&userVersion); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if userVersion != want {
		t.Fatalf("user_version = %d, want %d", userVersion, want)
	}
}

func assertForeignKeysClean(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `pragma foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign_key_check: %v", err)
	}
}

func assertAutoVacuumMode(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(), `pragma auto_vacuum`).Scan(&got); err != nil {
		t.Fatalf("query auto_vacuum: %v", err)
	}
	if got != want {
		t.Fatalf("auto_vacuum = %d, want %d", got, want)
	}
}
