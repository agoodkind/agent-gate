package auditmaintenance_test

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func TestWriteNextAttemptReplacesOnlySchedulerDeadline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		_ = database.Close()
		t.Fatalf("Migrate: %v", err)
	}
	dueAt := time.Date(2026, time.August, 11, 8, 0, 0, 0, time.UTC)
	dueAtRaw := dueAt.Format(time.RFC3339Nano)
	if _, err := database.ExecContext(t.Context(), `
		insert into audit_maintenance_runs (
			run_id, planned_at, started_at, completed_at, policy_hash,
			plan_json, result, next_due_at
		) values ('run', ?, ?, ?, 'hash', '{}', 'success', ?)
	`, dueAtRaw, dueAtRaw, dueAtRaw, dueAtRaw); err != nil {
		_ = database.Close()
		t.Fatalf("insert due run: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close SQLite: %v", err)
	}

	first := time.Date(2026, time.August, 12, 10, 0, 0, 0, time.UTC)
	second := first.Add(2 * time.Hour)
	if err := auditmaintenance.WriteNextAttempt(t.Context(), path, first); err != nil {
		t.Fatalf("WriteNextAttempt first: %v", err)
	}
	if err := auditmaintenance.WriteNextAttempt(t.Context(), path, second); err != nil {
		t.Fatalf("WriteNextAttempt second: %v", err)
	}

	database, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("reopen SQLite: %v", err)
	}
	defer func() { _ = database.Close() }()
	var nextRaw string
	var dueRaw string
	if err := database.QueryRowContext(t.Context(), `
		select schedule.next_attempt_at, run.next_due_at
		from audit_maintenance_schedule schedule
		cross join audit_maintenance_runs run
		where schedule.singleton = 1 and run.run_id = 'run'
	`).Scan(&nextRaw, &dueRaw); err != nil {
		t.Fatalf("read maintenance metadata: %v", err)
	}
	if nextRaw != second.Format(time.RFC3339Nano) {
		t.Fatalf("next attempt = %q, want %q", nextRaw, second.Format(time.RFC3339Nano))
	}
	if dueRaw != dueAtRaw {
		t.Fatalf("due at = %q, want %q", dueRaw, dueAtRaw)
	}
}

func TestV6MigrationDoesNotClearNoSuccessOverdueState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Add(5 * time.Minute)
	old := now.Add(-3 * time.Hour).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(t.Context(), `
		update audit_schema_migrations set applied_at = ?;
		drop table audit_maintenance_schedule;
		delete from audit_schema_migrations where version = 6;
		pragma user_version = 5;
	`, old); err != nil {
		t.Fatal(err)
	}
	policy := config.AuditStoragePolicy{MaintenanceInterval: time.Hour}
	before, err := auditmaintenance.ReadStatus(t.Context(), path, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if !before.Overdue {
		t.Fatal("v5 database was not overdue before restart migration")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	after, err := auditmaintenance.ReadStatus(t.Context(), path, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Overdue {
		t.Fatalf("v6 restart migration cleared overdue: due=%v next=%v", after.MaintenanceDueAt, after.NextAttemptAt)
	}
}

func TestFreshDatabaseUsesFirstStorageMigrationAsMaintenanceBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		t.Fatal(err)
	}
	firstAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), database, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	policy := config.AuditStoragePolicy{MaintenanceInterval: time.Hour}
	status, err := auditmaintenance.ReadStatus(
		t.Context(), path, policy, firstAppliedAt.Add(30*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantDueAt := firstAppliedAt.Add(time.Hour)
	if status.MaintenanceDueAt == nil || !status.MaintenanceDueAt.Equal(wantDueAt) {
		t.Fatalf("fresh database due at = %v, want %s", status.MaintenanceDueAt, wantDueAt)
	}
	if status.Overdue {
		t.Fatal("fresh database overdue = true before first interval")
	}
}
