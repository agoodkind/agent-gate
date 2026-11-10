package auditstorage_test

import (
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestBorrowedWriterDiscardsOnlyItsPhysicalConnectionAfterPoolExpansion(t *testing.T) {
	database, err := auditstorage.OpenWriter(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()
	borrowed, err := auditstorage.BorrowWriterConnection(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = borrowed.Close() }()
	database.SetMaxOpenConns(2)
	other, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = other.Close() }()
	var physical *auditstorage.SQLiteConnection
	if err := other.Raw(func(raw any) error {
		physical = raw.(*auditstorage.SQLiteConnection)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := borrowed.Discard(); err != nil {
		t.Fatal(err)
	}

	if err := other.Close(); err != nil {
		t.Fatal(err)
	}

	database.SetMaxOpenConns(1)
	remaining, err := auditstorage.BorrowWriterConnection(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}

	if err := remaining.Raw(func(raw any) error {
		if raw != physical {
			t.Fatal("discard invalidated another borrowed physical connection")
		}

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := remaining.Close(); err != nil {
		t.Fatal(err)
	}
}
