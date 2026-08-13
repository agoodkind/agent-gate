package auditstorage_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestDaemonUnresolvedCutoverNeverCreatesEmptyDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverPrepared,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := intake.OpenSQLite(t.Context(), path, nil)
	if err == nil || !strings.Contains(err.Error(), "full compaction recovery is required") {
		t.Fatalf("OpenSQLite error = %v, want recovery required", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("database stat error = %v, want missing", err)
	}
}

func TestDaemonCommittedCutoverUsesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverCommitted,
		CopyIdentity: identity,
	}); err != nil {
		t.Fatal(err)
	}
	store, err = intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	_ = store.Close()
}

func TestCommittedCutoverRejectsUnexpectedReplacementIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := os.WriteFile(path, []byte("unexpected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path,
		RunID:        "run",
		Phase:        auditstorage.CutoverCommitted,
		CopyIdentity: auditstorage.FileIdentity{Device: "wrong", Inode: "wrong"},
	}); err != nil {
		t.Fatal(err)
	}

	err := auditstorage.GuardDatabasePath(path)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("GuardDatabasePath error = %v, want replacement identity rejection", err)
	}
}

func TestCommittedCutoverRejectsChangedContentAtSameFilesystemIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	journal := validCutoverJournal(path)
	journal.Phase = auditstorage.CutoverCommitted
	journal.CopyIdentity = identity
	if err := auditstorage.WriteCutoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = auditstorage.GuardDatabasePath(path)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("GuardDatabasePath error = %v, want content identity rejection", err)
	}
}

func TestCommittedCutoverRejectsReplacementAfterAccessAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`create table marker(value text)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	journal := validCutoverJournal(path)
	journal.Phase = auditstorage.CutoverCommitted
	journal.CopyIdentity = identity
	if err := auditstorage.WriteCutoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	database, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.GuardDatabase(t.Context(), database); err != nil {
		t.Fatalf("authorize committed access: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	replacementPath := path + ".replacement"
	if err := os.WriteFile(replacementPath, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}

	err = auditstorage.GuardDatabasePath(path)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("GuardDatabasePath error = %v, want replacement identity rejection", err)
	}
}

func TestCommittedCutoverPathGuardDoesNotWriteAuthorization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	journal := validCutoverJournal(path)
	journal.Phase = auditstorage.CutoverCommitted
	journal.CopyIdentity = identity
	if err := auditstorage.WriteCutoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(auditstorage.CutoverJournalPath(path))
	if err != nil {
		t.Fatal(err)
	}

	if err := auditstorage.GuardDatabasePath(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(auditstorage.CutoverJournalPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("path guard changed committed journal")
	}
}

func TestAuthorizedCommittedPathGuardDoesNotReadDatabaseContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	if err := os.WriteFile(path, []byte("expected"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	journal := validCutoverJournal(path)
	journal.Phase = auditstorage.CutoverCommitted
	journal.CopyIdentity = identity
	journal.ReplacementAccessAuthorized = true
	if err := auditstorage.WriteCutoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })

	if err := auditstorage.GuardDatabasePath(path); err != nil {
		t.Fatalf("GuardDatabasePath: %v", err)
	}
}

func TestReadCutoverJournalRejectsInvalidVersionPhaseTrailingDataAndOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	tests := []struct {
		name string
		body func(t *testing.T) []byte
	}{
		{
			name: "wrong version",
			body: func(t *testing.T) []byte {
				journal := validCutoverJournal(path)
				journal.Version = 2
				return encodeCutoverJournal(t, journal)
			},
		},
		{
			name: "unknown phase",
			body: func(t *testing.T) []byte {
				journal := validCutoverJournal(path)
				journal.Phase = auditstorage.CutoverPhase("unknown")
				return encodeCutoverJournal(t, journal)
			},
		},
		{
			name: "trailing object",
			body: func(t *testing.T) []byte {
				journal := validCutoverJournal(path)
				body := encodeCutoverJournal(t, journal)
				return append(body, []byte("\n{}")...)
			},
		},
		{
			name: "oversize",
			body: func(t *testing.T) []byte {
				journal := validCutoverJournal(path)
				journal.ServiceBinary = strings.Repeat("a", 2<<20)
				return encodeCutoverJournal(t, journal)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(auditstorage.CutoverJournalPath(path), test.body(t), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := auditstorage.ReadCutoverJournal(path); err == nil {
				t.Fatal("ReadCutoverJournal error = nil, want strict validation error")
			}
		})
	}
}

func TestReadCutoverJournalRejectsPathsOutsideRunScopedSiblings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	journal := validCutoverJournal(path)
	journal.WorkingPath = filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(
		auditstorage.CutoverJournalPath(path),
		encodeCutoverJournal(t, journal),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if _, _, err := auditstorage.ReadCutoverJournal(path); err == nil {
		t.Fatal("ReadCutoverJournal error = nil, want run-scoped sibling rejection")
	}
}

func TestEveryAuditConstructorRejectsUnresolvedCutover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`create table marker(value text)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "run", Phase: auditstorage.CutoverOriginalRenamed,
	}); err != nil {
		t.Fatal(err)
	}
	database, err = sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if err := auditstorage.GuardDatabasePath(path); err == nil {
		t.Fatal("GuardDatabasePath error = nil, want recovery required")
	}
	if err := auditstorage.Migrate(t.Context(), database); err == nil {
		t.Fatal("Migrate error = nil, want recovery required")
	}
	if _, err := evaluation.NewStore(t.Context(), path, database); err == nil {
		t.Fatal("NewStore error = nil, want recovery required")
	}
	if _, err := intake.OpenSQLite(t.Context(), path, nil); err == nil {
		t.Fatal("OpenSQLite error = nil, want recovery required")
	}
}

func validCutoverJournal(path string) auditstorage.CutoverJournal {
	runID := "0123456789abcdef0123456789abcdef"
	prefix := path + ".compact." + runID
	return auditstorage.CutoverJournal{
		Version:      1,
		RunID:        runID,
		DatabasePath: path,
		WorkingPath:  prefix + ".working",
		RollbackPath: prefix + ".rollback",
		FailedPath:   prefix + ".failed",
		Phase:        auditstorage.CutoverPrepared,
	}
}

func encodeCutoverJournal(t *testing.T, journal auditstorage.CutoverJournal) []byte {
	t.Helper()
	body, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
