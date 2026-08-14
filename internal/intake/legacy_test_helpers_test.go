package intake_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
)

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

func assertSchemaVersion(t *testing.T, database *sql.DB, want int) {
	t.Helper()
	got, err := auditstorage.SchemaVersion(t.Context(), database)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	if got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
}
