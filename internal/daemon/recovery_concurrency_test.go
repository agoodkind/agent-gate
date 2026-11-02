package daemon_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestRecoveryConvertsFileWhileAnotherSessionRunsCurrentWork(t *testing.T) {
	harness := newOwnershipHarness(t, 2, false, false)
	harness.server.Close()
	bucket := seedRecoveryBacklog(t, harness, 2)
	restarted, err := daemon.New(t.Context(), ownershipLogger(), harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	harness.server = restarted
	restarted.StartAuditScheduler(t.Context())
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	harness.accept(t, "current-first")
	harness.accept(t, "current-second")
	close(first.release)
	current := harness.nextCall(t)
	if !strings.Contains(current.input, "current-") {
		t.Fatalf("expected current work: %s", current.input)
	}
	close(second.release)
	ownershipWait(t, func() bool { complete, err := auditstorage.Completed(bucket.Path); return err == nil && complete })
	select {
	case <-current.canceled:
		t.Fatal("finalization canceled unrelated current work")
	default:
	}
	close(current.release)
	last := harness.nextCall(t)
	close(last.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{4, 4, 4, 4} })
}

func TestRetentionKeepsCurrentWorkRunningWhileSelectedRenewalWaits(t *testing.T) {
	harness := newOwnershipHarness(t, 2, true, false)
	first := harness.nextCall(t)
	second := harness.nextCall(t)
	selected := first
	other := second
	if strings.Contains(second.input, "historical-2") {
		selected = second
		other = first
	}
	harness.maxSize = "4MiB"
	harness.reload(t, 2, "selected-cleanup")
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	catalog := ownershipCatalog(t, cfg)
	readers, err := catalog.Read(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	oldest := readers.Handles[0].Bucket
	if err := readers.Close(); err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), oldest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	transaction, err := handle.Database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(t.Context(), ownershipFixture(t, "ownership_padding.sql"), 2*cfg.AuditStoragePolicy().MaxBytes, "historical-2"); err != nil {
		t.Fatal(err)
	}
	bytes, err := auditstorage.FileBytes(oldest.Path)
	if err != nil || bytes <= cfg.AuditStoragePolicy().MaxBytes {
		t.Fatalf("selected file did not exceed retention size: %d: %v", bytes, err)
	}
	time.Sleep(100 * time.Millisecond)
	harness.accept(t, "unrelated-during-cleanup")
	select {
	case <-selected.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("retention did not cancel the selected inference")
	}
	close(other.release)
	select {
	case current := <-harness.service.entered:
		if !strings.Contains(current.input, "unrelated-during-cleanup") {
			t.Fatalf("unexpected work during selected cleanup: %s", current.input)
		}
		close(current.release)
	case <-time.After(time.Second):
		t.Fatal("selected renewal cleanup blocked unrelated current work")
	}
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg)[1] == 2 })
	if _, err := os.Stat(oldest.Path); err != nil {
		t.Fatalf("selected file disappeared before its writer released: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	ownershipWait(t, func() bool { _, err := os.Stat(oldest.Path); return os.IsNotExist(err) })
}
