package auditstorage

import (
	"database/sql"
	"testing"
)

func TestMaintenanceV5CreatesLeaseAndRunMetadata(t *testing.T) {
	database := openSchemaTestDatabase(t)
	if err := Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	version, err := SchemaVersion(t.Context(), database)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 6 {
		t.Fatalf("schema version = %d, want 6", version)
	}
	for _, table := range []string{"audit_maintenance_lease", "audit_maintenance_runs"} {
		if !schemaTableExists(t, database, table) {
			t.Fatalf("table %q is missing", table)
		}
	}
}

func TestMaintenanceV5IsIdempotent(t *testing.T) {
	database := openSchemaTestDatabase(t)
	if err := Migrate(t.Context(), database); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := Migrate(t.Context(), database); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	var count int
	if err := database.QueryRowContext(
		t.Context(),
		`select count(*) from audit_schema_migrations where version = 5`,
	).Scan(&count); err != nil {
		t.Fatalf("count migration records: %v", err)
	}
	if count != 1 {
		t.Fatalf("migration records = %d, want 1", count)
	}
}

func openSchemaTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func schemaTableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var count int
	if err := database.QueryRowContext(
		t.Context(),
		`select count(*) from sqlite_schema where type = 'table' and name = ?`,
		name,
	).Scan(&count); err != nil {
		t.Fatalf("inspect table %q: %v", name, err)
	}
	return count == 1
}
