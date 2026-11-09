package daemon_test

import (
	"os"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestReloadRotationRecoversOverflowInRetiredFile(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.queueLimit = 1
	harness.reload(t, 1, "one-queued")
	harness.accept(t, "warmup")
	warmup := harness.nextCall(t)
	close(warmup.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg)[1] == 1 })
	time.Sleep(100 * time.Millisecond)
	harness.accept(t, "blocked-before-reload")
	blocked := harness.nextCall(t)

	for _, identifier := range []string{"queued-before-reload", "overflow-first", "overflow-second"} {
		harness.accept(t, identifier)
	}

	catalog := ownershipCatalog(t, harness.cfg)
	status, err := catalog.Status(harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(
		status.CurrentBucketPath,
		harness.cfg.AuditStoragePolicy().Rotation().SegmentBytes(),
	); err != nil {
		t.Fatal(err)
	}

	harness.reload(t, 1, "forced-size-rotation")
	after, err := catalog.Status(harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil || after.CurrentBucketID == status.CurrentBucketID {
		t.Fatalf("reload did not rotate storage: %+v: %v", after, err)
	}

	close(blocked.release)

	for range 3 {
		call := harness.nextCall(t)
		close(call.release)
	}

	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{5, 5, 5, 5} })
	ownershipWait(
		t,
		func() bool {
			complete, err := auditstorage.Completed(status.CurrentBucketPath)
			return err == nil && complete
		},
	)
}

func TestRecoveryResumesAfterSizeRetentionFenceClears(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.maxSize = "4MiB"
	harness.reload(t, 1, "fence")
	harness.server.Close()
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}

	harness.cfg = cfg
	bucket := seedRecoveryBacklog(t, harness, 1)
	if err := os.Truncate(bucket.Path, 2*1024*1024); err != nil {
		t.Fatal(err)
	}

	catalog := ownershipCatalog(t, cfg)
	future, err := catalog.EnsureCurrent(
		t.Context(),
		cfg.AuditStoragePolicy().Rotation(),
		time.Now().Add(24*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Truncate(future.Path, 3*1024*1024); err != nil {
		t.Fatal(err)
	}

	reader, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reader.Close() }()
	server, err := daemon.New(t.Context(), ownershipLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.StartAuditScheduler(t.Context())
	call := harness.nextCall(t)
	if _, err := os.Stat(future.Path); !os.IsNotExist(err) {
		t.Fatalf("expired file survived pruning: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	close(call.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{1, 1, 1, 1} })
	ownershipWait(
		t,
		func() bool {
			complete, err := auditstorage.Completed(bucket.Path)
			return err == nil && complete
		},
	)
}
