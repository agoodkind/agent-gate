package intake_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	assertSchemaVersion(t, store.Handle(), 6)
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

func TestOpenSQLiteQuarantinesLegacyOrphanEvaluations(t *testing.T) {
	path := installLegacyOrphanAuditFixture(t)

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite legacy orphan migration: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	if got := queryRowCount(t, store.Handle(), "gate_evaluations"); got != 1 {
		t.Fatalf("canonical evaluations = %d, want 1", got)
	}
	if got := queryRowCount(
		t,
		store.Handle(),
		"audit_migration_quarantined_evaluations",
	); got != 20 {
		t.Fatalf("quarantined evaluations = %d, want 20", got)
	}
	if got := queryRowCount(
		t,
		store.Handle(),
		"audit_migration_quarantined_evaluation_layers",
	); got != 1 {
		t.Fatalf("quarantined evaluation layers = %d, want 1", got)
	}
	if got := queryRowCount(
		t,
		store.Handle(),
		"audit_migration_quarantined_evaluation_labels",
	); got != 1 {
		t.Fatalf("quarantined evaluation labels = %d, want 1", got)
	}
	assertLegacyOrphanQuarantineFidelity(t, store.Handle())
	assertForeignKeysClean(t, store.Handle())
}

func TestOpenSQLiteQuarantinesLegacyOrphanEvaluationOutbox(t *testing.T) {
	path := installLegacyOrphanAuditFixture(t)
	installLegacyFixtureFile(t, path, "legacy_orphan_outbox.sql")

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite legacy orphan outbox migration: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	if got := queryRowCount(
		t,
		store.Handle(),
		"audit_migration_quarantined_outbox",
	); got != 1 {
		t.Fatalf("quarantined outboxes = %d, want 1", got)
	}
	if got := queryRowCount(
		t,
		store.Handle(),
		"audit_migration_quarantined_outbox_entries",
	); got != 1 {
		t.Fatalf("quarantined outbox entries = %d, want 1", got)
	}
	var payload []byte
	if err := store.Handle().QueryRowContext(t.Context(), `
		select payload_json
		from audit_migration_quarantined_outbox_entries
		where receipt_id = 1001 and entry_index = 0
	`).Scan(&payload); err != nil {
		t.Fatalf("read quarantined outbox payload: %v", err)
	}
	wantPayload := []byte{0x00, 0xff, '{', '"', 'o', 'r', 'p', 'h', 'a', 'n', '"', ':', 't', 'r', 'u', 'e', '}'}
	if !reflect.DeepEqual(payload, wantPayload) {
		t.Fatalf("quarantined outbox payload = %x, want %x", payload, wantPayload)
	}
	assertForeignKeysClean(t, store.Handle())
}

func TestOpenSQLiteLegacyOrphanQuarantineIsIdempotent(t *testing.T) {
	path := installLegacyOrphanAuditFixture(t)
	first, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite first: %v", err)
	}
	firstAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), first.Handle(), 1)
	if err != nil {
		_ = first.Close()
		t.Fatalf("MigrationAppliedAt first: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close first: %v", err)
	}

	second, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite second: %v", err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Fatalf("Close second: %v", err)
		}
	})
	secondAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), second.Handle(), 1)
	if err != nil {
		t.Fatalf("MigrationAppliedAt second: %v", err)
	}
	if !secondAppliedAt.Equal(firstAppliedAt) {
		t.Fatalf("migration timestamp changed from %s to %s", firstAppliedAt, secondAppliedAt)
	}
	if got := queryRowCount(
		t,
		second.Handle(),
		"audit_migration_quarantined_evaluations",
	); got != 20 {
		t.Fatalf("quarantined evaluations after reopen = %d, want 20", got)
	}
}

func TestOpenSQLiteEnforcesForeignKeysAfterLegacyOrphanQuarantine(t *testing.T) {
	path := installLegacyOrphanAuditFixture(t)
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	_, err = store.Handle().ExecContext(t.Context(), `
		insert into gate_evaluations
		select 'eval-new-orphan', 9001, 'event-new-orphan', attempt, mode,
			config_hash, engine_version, engine_commit, engine_build_hash,
			input_hash, started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		from gate_evaluations
		where evaluation_id = 'eval-legacy'
	`)
	if err == nil || !strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
		t.Fatalf("new orphan insert error = %v, want foreign key rejection", err)
	}
}

func TestOpenSQLiteRejectsUnrecognizedLegacyForeignKeyViolation(t *testing.T) {
	path := installLegacyAuditFixture(t)
	installLegacyFixtureFile(t, path, "legacy_unknown_violation.sql")

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err == nil {
		_ = store.Close()
		t.Fatal("OpenSQLite error = nil, want unrecognized foreign key violation")
	}
	if !strings.Contains(err.Error(), "foreign key violation in intake_receipts") {
		t.Fatalf("OpenSQLite error = %v, want intake_receipts violation", err)
	}
	database := openSQLiteHandle(t, path)
	defer func() { _ = database.Close() }()
	if got := queryRowCount(t, database, "intake_receipts"); got != 2 {
		t.Fatalf("intake receipts after failure = %d, want 2", got)
	}
	assertSchemaVersion(t, database, 0)
	assertSchemaObjectMissing(t, database, "audit_migration_quarantined_evaluations")
}

func TestOpenSQLiteRollsBackLegacyOrphanQuarantineOnLaterFailure(t *testing.T) {
	path := installLegacyOrphanAuditFixture(t)
	database := openSQLiteHandle(t, path)
	if _, err := database.ExecContext(
		t.Context(),
		`create table violations_mode_idx (id integer primary key)`,
	); err != nil {
		_ = database.Close()
		t.Fatalf("install late migration failure: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close failure fixture: %v", err)
	}

	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err == nil {
		_ = store.Close()
		t.Fatal("OpenSQLite error = nil, want late migration failure")
	}
	database = openSQLiteHandle(t, path)
	defer func() { _ = database.Close() }()
	if got := queryRowCount(t, database, "gate_evaluations"); got != 21 {
		t.Fatalf("canonical evaluations after rollback = %d, want 21", got)
	}
	assertSchemaVersion(t, database, 0)
	assertSchemaObjectMissing(t, database, "audit_migration_quarantined_evaluations")
}

func assertLegacyOrphanQuarantineFidelity(t *testing.T, database *sql.DB) {
	t.Helper()
	var sourceRowID int64
	var receiptID int64
	var eventID string
	var reason string
	var errorJSON []byte
	if err := database.QueryRowContext(t.Context(), `
		select source_rowid, receipt_id, event_id, reason, error_json
		from audit_migration_quarantined_evaluations
		where evaluation_id = 'eval-orphan-01'
	`).Scan(&sourceRowID, &receiptID, &eventID, &reason, &errorJSON); err != nil {
		t.Fatalf("read quarantined evaluation: %v", err)
	}
	if sourceRowID <= 0 || receiptID != 1001 || eventID != "event-orphan-01" ||
		reason != "missing intake event and receipt" || string(errorJSON) != `{"orphan":1}` {
		t.Fatalf(
			"quarantined evaluation = row %d receipt %d event %q reason %q error %q",
			sourceRowID,
			receiptID,
			eventID,
			reason,
			errorJSON,
		)
	}
	var inputJSON []byte
	var outputJSON []byte
	var metadataJSON []byte
	var errorMessage string
	if err := database.QueryRowContext(t.Context(), `
		select input_json, output_json, metadata_json, error_message
		from audit_migration_quarantined_evaluation_layers
		where evaluation_id = 'eval-orphan-01' and layer_index = 0
	`).Scan(&inputJSON, &outputJSON, &metadataJSON, &errorMessage); err != nil {
		t.Fatalf("read quarantined evaluation layer: %v", err)
	}
	if string(inputJSON) != `{"input":"orphan"}` ||
		string(outputJSON) != `{"output":"orphan"}` ||
		string(metadataJSON) != `{"metadata":"orphan"}` ||
		errorMessage != "orphan layer error" {
		t.Fatalf(
			"quarantined layer = input %q output %q metadata %q error %q",
			inputJSON,
			outputJSON,
			metadataJSON,
			errorMessage,
		)
	}
	var rationale string
	if err := database.QueryRowContext(t.Context(), `
		select rationale
		from audit_migration_quarantined_evaluation_labels
		where evaluation_id = 'eval-orphan-01'
	`).Scan(&rationale); err != nil {
		t.Fatalf("read quarantined evaluation label: %v", err)
	}
	if rationale != "orphan label rationale" {
		t.Fatalf("quarantined label rationale = %q", rationale)
	}
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

func installLegacyOrphanAuditFixture(t *testing.T) string {
	t.Helper()
	path := installLegacyAuditFixture(t)
	installLegacyFixtureFile(t, path, "legacy_orphan_evaluations.sql")
	return path
}

func installLegacyFixtureFile(t *testing.T, path string, name string) {
	t.Helper()
	fixture, err := os.ReadFile(
		filepath.Join("..", "auditstorage", "testdata", name),
	)
	if err != nil {
		t.Fatalf("read legacy fixture %s: %v", name, err)
	}
	database := openSQLiteHandle(t, path)
	if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
		_ = database.Close()
		t.Fatalf("install legacy fixture %s: %v", name, err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy fixture %s: %v", name, err)
	}
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

func assertSchemaObjectMissing(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var got string
	err := database.QueryRowContext(
		t.Context(),
		`select name from sqlite_schema where name = ?`,
		name,
	).Scan(&got)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("schema object %q lookup error = %v, want sql.ErrNoRows", name, err)
	}
}
