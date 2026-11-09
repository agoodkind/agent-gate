package daemon_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func TestRecoveryRetriesHistoricalTraversalAfterDirectoryRestore(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	directory := filepath.Join(t.TempDir(), "sqlite")
	harness.base = filepath.Join(directory, "audit.db")
	harness.writeConfig(t, 1, "directory-retry")
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	harness.cfg = cfg
	catalog := ownershipCatalog(t, cfg)
	if _, err := catalog.Reset(
		t.Context(),
		cfg.AuditStoragePolicy().Rotation(),
		time.Now().Add(-24*time.Hour),
	); err != nil {
		t.Fatal(err)
	}

	historical := seedRecoveryBacklog(t, harness, 1)
	logPath := filepath.Join(t.TempDir(), "directory.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewTextHandler(logFile, nil))
	previousLog := slog.Default()
	slog.SetDefault(log)
	defer slog.SetDefault(previousLog)
	server, err := daemon.New(t.Context(), log, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	harness.server = server
	initial, err := catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	unavailable := directory + "-unavailable"
	if err := os.Rename(directory, unavailable); err != nil {
		t.Fatal(err)
	}

	restored := false
	defer func() {
		if !restored {
			if err := os.Rename(unavailable, directory); err != nil {
				t.Error(err)
			}
		}
	}()
	server.StartAuditScheduler(t.Context())
	ownershipWait(
		t,
		func() bool {
			body := readRecoveryLog(t, logPath)

			return strings.Contains(body, "discover audit files failed") &&
				strings.Contains(body, "open recovery directory") &&
				strings.Contains(body, "no such file or directory")
		},
	)
	failedAt := time.Now()
	// Keep the directory absent across a scheduler tick to detect immediate retries.
	time.Sleep(1100 * time.Millisecond)
	if count := strings.Count(
		readRecoveryLog(t, logPath),
		"discover audit files failed",
	); count != 1 {
		t.Fatalf("missing directory caused %d traversal warnings before the retry deadline", count)
	}

	t.Logf("observed missing-directory traversal failure: %s", readRecoveryLog(t, logPath))
	if err := os.Rename(unavailable, directory); err != nil {
		t.Fatal(err)
	}

	restored = true
	harness.accept(t, "current-after-directory-repair")
	current := harness.nextCall(t)
	if !strings.Contains(current.input, "current-after-directory-repair") {
		t.Fatalf("unexpected inference before retry: %s", current.input)
	}

	close(current.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg)[1] == 1 })
	t.Log("current receipt completed after restoring the directory")
	// Allow fifteen seconds for dispatch beyond the thirty-second retry delay.
	timer := time.NewTimer(time.Until(failedAt.Add(45 * time.Second)))
	defer timer.Stop()
	select {
	case recovered := <-harness.service.entered:
		if !strings.Contains(recovered.input, "backlog-0") {
			t.Fatalf("unexpected recovery inference: %s", recovered.input)
		}

		close(recovered.release)
		ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{2, 2, 2, 2} })
		final, err := catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now())
		if err != nil {
			t.Fatal(err)
		}

		if final.CurrentBucketPath != initial.CurrentBucketPath {
			t.Fatal("rotation supplied an unrelated historical notification")
		}

		t.Logf(
			"historical receipt, evaluation, and audit outcome completed after %s; current bucket unchanged",
			time.Since(failedAt),
		)
	case <-timer.C:
		handle, err := catalog.OpenWriter(t.Context(), historical)
		if err != nil {
			t.Fatal(err)
		}

		defer func() { _ = handle.Close() }()
		store, err := intake.NewStore(
			t.Context(),
			handle.Database,
			cfg.AuditStoragePolicy(),
			ownershipLogger(),
		)
		if err != nil {
			t.Fatal(err)
		}

		pending, err := store.HasPending(t.Context())
		if err != nil {
			t.Fatal(err)
		}

		t.Fatalf(
			"missing-directory historical retry did not run after %s; durable_pending=%v; outcomes=%v; log=%s",
			time.Since(failedAt),
			pending,
			ownershipOutcomes(t, cfg),
			readRecoveryLog(t, logPath),
		)
	}
}
