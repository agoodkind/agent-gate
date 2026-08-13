package audit

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func TestAuditSinkRejectsUnresolvedCutoverBeforeDirectoryOrDatabaseCreation(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nested", "audit.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverPrepared,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := newSQLiteEventSink(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "recovery is required") {
		t.Fatalf("newSQLiteEventSink error = %v, want recovery required", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database stat error = %v, want missing", err)
	}
}

func TestAuditSinkSharedHandleMigrationRejectsUnresolvedCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if _, err := database.Exec(`create table marker(value text)`); err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverInstalling,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := newSQLiteEventSinkFromDB(t.Context(), path, database, nil); err == nil {
		t.Fatal("newSQLiteEventSinkFromDB error = nil, want recovery required")
	}
}

func TestAuditQueryRejectsUnresolvedCutoverWhenDatabaseIsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverOriginalRenamed,
	}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{
		SQLite: config.AuditSQLiteOutput{Path: path},
	}}}

	_, _, err := Query(cfg, QueryFilter{})
	if err == nil || !strings.Contains(err.Error(), "recovery is required") {
		t.Fatalf("Query error = %v, want recovery required", err)
	}
}
