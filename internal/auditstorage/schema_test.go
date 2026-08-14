package auditstorage_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestMigrationRecordsVersionAndAppliedTimeOnce(t *testing.T) {
	database := openMigrationDatabase(t)

	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate first: %v", err)
	}
	firstAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), database, 1)
	if err != nil {
		t.Fatalf("MigrationAppliedAt first: %v", err)
	}
	if firstAppliedAt.IsZero() {
		t.Fatal("MigrationAppliedAt first = zero")
	}

	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate second: %v", err)
	}
	secondAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), database, 1)
	if err != nil {
		t.Fatalf("MigrationAppliedAt second: %v", err)
	}
	if !secondAppliedAt.Equal(firstAppliedAt) {
		t.Fatalf("applied at changed from %s to %s", firstAppliedAt, secondAppliedAt)
	}
}

func TestMigrationFailureRollsBackVersionAndApplicationSchema(t *testing.T) {
	database := openMigrationDatabase(t)
	if _, err := database.ExecContext(
		t.Context(),
		`create table violations_mode_idx (id integer primary key)`,
	); err != nil {
		t.Fatalf("install late migration failure: %v", err)
	}

	if err := auditstorage.Migrate(t.Context(), database); err == nil {
		t.Fatal("Migrate error = nil, want late schema failure")
	}
	version, err := auditstorage.SchemaVersion(t.Context(), database)
	if err != nil {
		t.Fatalf("SchemaVersion after failure: %v", err)
	}
	if version != 0 {
		t.Fatalf("schema version after failure = %d, want 0", version)
	}
	assertUserVersion(t, database, 0)
	assertSchemaObjectAbsent(t, database, "audit_schema_migrations")
	assertSchemaObjectAbsent(t, database, "intake_events")
	assertSchemaObjectAbsent(t, database, "events")
}

func TestMigrationEnablesIncrementalAutoVacuumForNewDatabase(t *testing.T) {
	database := openMigrationDatabase(t)

	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	var mode int
	if err := database.QueryRowContext(t.Context(), `pragma auto_vacuum`).Scan(&mode); err != nil {
		t.Fatalf("query auto_vacuum: %v", err)
	}
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want incremental mode 2", mode)
	}
}

func TestMigrationRejectsExistingDatabase(t *testing.T) {
	database := openMigrationDatabase(t)
	if _, err := database.ExecContext(t.Context(), `create table legacy_data (id integer)`); err != nil {
		t.Fatalf("create existing table: %v", err)
	}

	err := auditstorage.Migrate(t.Context(), database)
	if err == nil || err.Error() != "existing audit databases are not supported; start with an empty database" {
		t.Fatalf("Migrate error = %v", err)
	}

	assertSchemaObjectAbsent(t, database, "audit_schema_migrations")
}

func openMigrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})
	return database
}

func assertUserVersion(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	var got int
	if err := database.QueryRowContext(t.Context(), `pragma user_version`).Scan(&got); err != nil {
		t.Fatalf("query user_version: %v", err)
	}
	if got != want {
		t.Fatalf("user_version = %d, want %d", got, want)
	}
}

func assertSchemaObjectAbsent(t *testing.T, database *sql.DB, name string) {
	t.Helper()
	var objectName string
	err := database.QueryRowContext(
		t.Context(),
		`select name from sqlite_schema where name = ?`,
		name,
	).Scan(&objectName)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("schema object %q lookup error = %v, want sql.ErrNoRows", name, err)
	}
}
