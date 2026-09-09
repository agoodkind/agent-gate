package auditstorage_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestOpenWriterReopensWithConnectionSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	for range 2 {
		database, err := auditstorage.OpenWriter(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		if database.Stats().MaxOpenConnections != 1 {
			t.Fatal("writer pool is not serialized")
		}
		database.SetMaxIdleConns(0)
		for range 2 {
			var foreignKeys, synchronous, timeout int
			var journal string
			if err := database.QueryRowContext(t.Context(), readFixture(t, "connection_settings.sql")).Scan(&foreignKeys, &journal, &synchronous, &timeout); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 || journal != "wal" || synchronous != 1 || timeout != 5000 {
				t.Fatalf("connection settings=%d/%s/%d/%d", foreignKeys, journal, synchronous, timeout)
			}
		}
		if err := audit.WriteEvents(t.Context(), database, []audit.Event{{EventID: "reopen", SchemaVersion: 1}}); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInitializeRollsBackPartialSchema(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.ExecContext(t.Context(), readFixture(t, "conflicting_schema.sql")); err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.Initialize(t.Context(), database); err == nil {
		t.Fatal("accepted conflicting schema")
	}
	var tables int
	if err := database.QueryRowContext(t.Context(), readFixture(t, "application_table_count.sql")).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 1 {
		t.Fatalf("partial schema survived rollback: %d tables", tables)
	}
}

func TestAuditChildOwnershipCascadesAndRejectsOrphans(t *testing.T) {
	database, err := auditstorage.OpenWriter(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	event := audit.Event{EventID: "parent", SchemaVersion: 1, Violations: []audit.Violation{{Rule: "rule"}}}
	if err := audit.WriteEvents(t.Context(), database, []audit.Event{event}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), readFixture(t, "delete_audit_parent.sql")); err != nil {
		t.Fatal(err)
	}
	var children int
	if err := database.QueryRowContext(t.Context(), readFixture(t, "audit_child_count.sql")).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if children != 0 {
		t.Fatalf("orphan children=%d", children)
	}
	if _, err := database.ExecContext(t.Context(), readFixture(t, "insert_orphan.sql")); err == nil {
		t.Fatal("accepted orphan decision")
	}
}
