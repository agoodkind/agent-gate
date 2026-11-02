package auditstorage_test

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestInterruptedJournalConversionRecoversAfterRestart(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	database, err := auditstorage.OpenWriter(t.Context(), old.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if err := audit.WriteEvents(t.Context(), database, []audit.Event{{EventID: "preserved", SchemaVersion: 1}}); err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	var count int
	if err := transaction.QueryRowContext(t.Context(), readFixture(t, "application_table_count.sql")).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if complete, err := catalog.Finalize(t.Context(), old); complete || !errors.Is(err, auditstorage.ErrFileBusy) {
		t.Fatalf("interruption = %v, %v", complete, err)
	}
	if complete, err := auditstorage.Completed(old.Path); complete || err != nil {
		t.Fatalf("interruption changed durable completion: %v, %v", complete, err)
	}
	root := filepath.Dir(old.Path)
	restarted, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{BasePath: filepath.Join(root, "audit.db"), StatePath: filepath.Join(root, "state.json"), CoordinationPath: filepath.Join(root, "catalog.lock")})
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if complete, err := restarted.Finalize(t.Context(), old); !complete || err != nil {
		t.Fatalf("restart conversion = %v, %v", complete, err)
	}
	if complete, err := restarted.Finalize(t.Context(), old); !complete || err != nil {
		t.Fatalf("repeated conversion = %v, %v", complete, err)
	}
}
