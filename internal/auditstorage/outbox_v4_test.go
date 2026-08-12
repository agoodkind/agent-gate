package auditstorage

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func TestOutboxV4MigratesPayloadIntoDetail(t *testing.T) {
	database := openOutboxMigrationDatabase(t)
	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy_v1.sql"))
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatalf("install legacy fixture: %v", err)
	}

	if err := Migrate(t.Context(), database); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	version, err := SchemaVersion(t.Context(), database)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if version != 5 {
		t.Fatalf("schema version = %d, want 5", version)
	}
	var payload []byte
	if err := database.QueryRowContext(t.Context(), `
		select payload_json from deferred_audit_outbox_entry_details
		where receipt_id = 1 and entry_index = 0
	`).Scan(&payload); err != nil {
		t.Fatalf("read migrated outbox detail: %v", err)
	}
	if string(payload) != "{}" {
		t.Fatalf("migrated payload = %q, want %q", payload, "{}")
	}
	var recorded int
	var available int
	var changedAt string
	if err := database.QueryRowContext(t.Context(), `
		select payload_recorded, payload_available, payload_state_changed_at
		from deferred_audit_outbox_entries
		where receipt_id = 1 and entry_index = 0
	`).Scan(&recorded, &available, &changedAt); err != nil {
		t.Fatalf("read migrated outbox header: %v", err)
	}
	if recorded != 1 || available != 1 || changedAt == "" {
		t.Fatalf(
			"migrated payload state = recorded %d available %d changed %q",
			recorded,
			available,
			changedAt,
		)
	}
	var payloadColumnCount int
	if err := database.QueryRowContext(t.Context(), `
		select count(*) from pragma_table_info('deferred_audit_outbox_entries')
		where name = 'payload_json'
	`).Scan(&payloadColumnCount); err != nil {
		t.Fatalf("inspect outbox header columns: %v", err)
	}
	if payloadColumnCount != 0 {
		t.Fatalf("outbox header payload columns = %d, want 0", payloadColumnCount)
	}
}

func TestOutboxV4PreservesCopiedDetailOnMigrationConnection(t *testing.T) {
	database := openOutboxMigrationDatabase(t)
	installOutboxLegacyFixture(t, database)
	for _, migration := range migrations[:3] {
		if err := applyMigration(t.Context(), database, migration); err != nil {
			t.Fatalf("apply migration %d: %v", migration.Version, err)
		}
	}

	disabledConnection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve foreign-keys-disabled connection: %v", err)
	}
	defer func() { _ = disabledConnection.Close() }()
	if _, err := disabledConnection.ExecContext(t.Context(), `pragma foreign_keys = off`); err != nil {
		t.Fatalf("disable foreign keys on first connection: %v", err)
	}
	migrationConnection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve migration connection: %v", err)
	}
	defer func() { _ = migrationConnection.Close() }()
	if _, err := migrationConnection.ExecContext(t.Context(), `pragma foreign_keys = on`); err != nil {
		t.Fatalf("enable foreign keys on migration connection: %v", err)
	}

	if err := applyMigrationOnConnection(
		t.Context(),
		migrationConnection,
		migrations[3],
	); err != nil {
		t.Fatalf("apply version 4 migration: %v", err)
	}
	var detailCount int
	if err := migrationConnection.QueryRowContext(t.Context(), `
		select count(*) from deferred_audit_outbox_entry_details
		where receipt_id = 1 and entry_index = 0
	`).Scan(&detailCount); err != nil {
		t.Fatalf("count copied outbox detail: %v", err)
	}
	if detailCount != 1 {
		t.Fatalf("copied outbox detail rows = %d, want 1", detailCount)
	}
	var foreignKeysEnabled int
	if err := migrationConnection.QueryRowContext(
		t.Context(),
		`pragma foreign_keys`,
	).Scan(&foreignKeysEnabled); err != nil {
		t.Fatalf("read restored foreign key state: %v", err)
	}
	if foreignKeysEnabled != 1 {
		t.Fatalf("foreign keys after migration = %d, want 1", foreignKeysEnabled)
	}
}

func TestMigrationRestoresForeignKeysAfterCancellation(t *testing.T) {
	database := openOutboxMigrationDatabase(t)
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve migration connection: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(t.Context(), `pragma foreign_keys = on`); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	migrationContext, cancel := context.WithCancel(t.Context())
	migrationErr := errors.New("cancel migration")
	migration := Migration{
		Version:             99,
		ForeignKeysDisabled: true,
		AfterCommit:         nil,
		Apply: func(context.Context, *sql.Tx) error {
			cancel()
			return migrationErr
		},
	}
	err = applyMigrationOnConnection(migrationContext, connection, migration)
	if !errors.Is(err, migrationErr) {
		t.Fatalf("migration error = %v, want %v", err, migrationErr)
	}
	var foreignKeysEnabled int
	if err := connection.QueryRowContext(
		t.Context(),
		`pragma foreign_keys`,
	).Scan(&foreignKeysEnabled); err != nil {
		t.Fatalf("read foreign key state after cancellation: %v", err)
	}
	if foreignKeysEnabled != 1 {
		t.Fatalf("foreign keys after cancellation = %d, want 1", foreignKeysEnabled)
	}
}

func openOutboxMigrationDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.SetMaxOpenConns(2)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	return database
}

func installOutboxLegacyFixture(t *testing.T, database *sql.DB) {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("testdata", "legacy_v1.sql"))
	if err != nil {
		t.Fatalf("read legacy fixture: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatalf("install legacy fixture: %v", err)
	}
}
