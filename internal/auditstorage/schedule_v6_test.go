package auditstorage

import "testing"

func TestScheduleV6CreatesSingletonNextAttemptMetadata(t *testing.T) {
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
	if !schemaTableExists(t, database, "audit_maintenance_schedule") {
		t.Fatal("table audit_maintenance_schedule is missing")
	}
}

func TestScheduleV6AllowsOnlySingletonRow(t *testing.T) {
	database := openSchemaTestDatabase(t)
	if err := Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into audit_maintenance_schedule(singleton, next_attempt_at)
		values (2, '2026-08-12T10:00:00Z')
	`); err == nil {
		t.Fatal("insert non-singleton schedule row succeeded")
	}
}
