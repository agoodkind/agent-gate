package auditstorage_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func assertCompletedSidecarsAbsent(t *testing.T, path string) {
	t.Helper()

	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("completed sidecar %s: %v", suffix, err)
		}
	}
}

func TestCompletedFileWaitsForPendingClaimsAndReaders(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}

	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}

	store, err := intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := store.Append(
		t.Context(),
		intake.Record{EventID: "pending", RawPayload: []byte(`{}`)},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.MarkDeferredPending(
		t.Context(),
		receipt.EventID,
		receipt.ReceiptID,
	); err != nil {
		t.Fatal(err)
	}

	_, claim, err := store.ClaimDeferred(t.Context(), receipt.ReceiptID, "live", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.Finalize(t.Context(), old); !errors.Is(err, auditstorage.ErrFileBusy) {
		t.Fatalf("writer finalization = %v", err)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if complete, err := catalog.Finalize(t.Context(), old); complete || err != nil {
		t.Fatalf("live claim finalization = %v, %v", complete, err)
	}

	handle, err = catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}

	store, err = intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.ScheduleDeferredRetry(
		t.Context(),
		claim,
		time.Now().Add(time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	if complete, err := catalog.Finalize(t.Context(), old); complete || err != nil {
		t.Fatalf("future retry finalization = %v, %v", complete, err)
	}

	handle, err = catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}

	store, err = intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.MarkDeferredComplete(t.Context(), receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}

	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	readers, err := catalog.Read(t.Context(), policy, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.Finalize(t.Context(), old); !errors.Is(err, auditstorage.ErrFileBusy) {
		t.Fatalf("reader finalization = %v", err)
	}

	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}

	if complete, err := catalog.Finalize(t.Context(), old); !complete || err != nil {
		t.Fatalf("completed conversion = %v, %v", complete, err)
	}

	assertCompletedSidecarsAbsent(t, old.Path)

	for range 3 {
		readers, err := catalog.Read(t.Context(), policy, now.Add(24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		for _, reader := range readers.Handles {
			if reader.Bucket != old {
				continue
			}

			var count int
			if err := reader.Database.QueryRowContext(
				t.Context(),
				readFixture(t, "catalog_event_count.sql"),
				receipt.EventID,
			).Scan(
				&count,
			); err != nil ||
				count != 1 {
				t.Fatalf("retained receipt = %d: %v", count, err)
			}
		}

		if err := readers.Close(); err != nil {
			t.Fatal(err)
		}

		assertCompletedSidecarsAbsent(t, old.Path)
	}

	if _, err := catalog.OpenWriter(t.Context(), old); !errors.Is(
		err,
		auditstorage.ErrFileComplete,
	) {
		t.Fatalf("completed writer = %v", err)
	}

	reselected, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil || reselected.Path == old.Path {
		t.Fatalf("backward clock reactivated completed file: %+v, %v", reselected, err)
	}

	assertCompletedSidecarsAbsent(t, old.Path)
}
