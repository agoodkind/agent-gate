package auditstorage_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func sizeCatalog(t *testing.T) (*auditstorage.Catalog, auditstorage.RotationPolicy, time.Time) {
	t.Helper()
	root := t.TempDir()
	catalog, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{
		BasePath:         filepath.Join(root, "audit.db"),
		StatePath:        filepath.Join(root, "state.json"),
		CoordinationPath: filepath.Join(root, "catalog.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return catalog, auditstorage.RotationPolicy{Interval: 24 * time.Hour, Retained: 0, MaxBytes: 4 * 1024 * 1024}, time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
}

func writeSizedIntake(t *testing.T, catalog *auditstorage.Catalog, bucket auditstorage.Bucket, eventID string) {
	t.Helper()
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	store, err := intake.NewStore(t.Context(), handle.Database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(t.Context(), intake.Record{
		EventID: eventID, System: "codex", SessionID: "size", EventName: "PreToolUse",
		RawPayload: []byte(strings.Repeat("x", 1024*1024)), NormalizedJSON: []byte(`{}`),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSizePruningDeletesOldestSegmentsAndPreservesRecentRecords(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	var buckets []auditstorage.Bucket
	for index := range 6 {
		bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
		if err != nil {
			t.Fatal(err)
		}
		writeSizedIntake(t, catalog, bucket, fmt.Sprintf("event-%d", index))
		buckets = append(buckets, bucket)
	}
	active, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if active.ID == buckets[len(buckets)-1].ID || active.Start != buckets[0].Start {
		t.Fatalf("same-day rotation = %+v", active)
	}
	before, err := catalog.Status(policy, now)
	if err != nil || before.TotalBytes <= policy.MaxBytes {
		t.Fatalf("before prune = %+v, %v", before, err)
	}
	result, err := catalog.Prune(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) == 0 || result.Removed[0] != buckets[0].Path {
		t.Fatalf("oldest-first pruning = %+v", result)
	}
	for _, path := range result.Removed {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed file remains: %s: %v", path, err)
		}
	}
	after, err := catalog.Status(policy, now)
	if err != nil || after.TotalBytes > policy.MaxBytes || after.ExcessBytes != 0 || after.CurrentBucketID != active.ID {
		t.Fatalf("after prune = %+v, %v", after, err)
	}
	readers, err := catalog.Read(t.Context(), policy, now.Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readers.Close() }()
	query, err := os.ReadFile("testdata/catalog_event_count.sql")
	if err != nil {
		t.Fatal(err)
	}
	var newestCount int
	for _, handle := range readers.Handles {
		var count int
		if err := handle.Database.QueryRowContext(t.Context(), string(query), "event-5").Scan(&count); err != nil {
			t.Fatal(err)
		}
		newestCount += count
	}
	if newestCount != 1 {
		t.Fatalf("newest record after size-only pruning = %d", newestCount)
	}
}

func TestSizePruningWaitsForOldestReaderAndReportsActualBytes(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	var oldest auditstorage.Bucket
	for index := range 5 {
		bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			oldest = bucket
		}
		writeSizedIntake(t, catalog, bucket, fmt.Sprintf("locked-%d", index))
	}
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	reader, err := catalog.OpenWriter(t.Context(), oldest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	result, err := catalog.Prune(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 || len(result.Deferred) != 1 || result.Deferred[0] != oldest.Path {
		t.Fatalf("prune behind oldest lock = %+v", result)
	}
	status, err := catalog.Status(policy, now)
	if err != nil || status.ExcessBytes <= 0 || len(status.DeferredFiles) != 1 {
		t.Fatalf("deferred status = %+v, %v", status, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Prune(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	status, err = catalog.Status(policy, now)
	if err != nil || status.ExcessBytes != 0 || len(status.DeferredFiles) != 0 {
		t.Fatalf("status after reader closes = %+v, %v", status, err)
	}
}

func TestRetentionChangesPreserveFilesAndRestartUsesLatestSegment(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	writeSizedIntake(t, catalog, old, "retained")
	latest, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	policy.Retained = 7
	policy.MaxBytes *= 2
	root := filepath.Dir(old.Path)
	catalog, err = auditstorage.NewCatalog(auditstorage.CatalogOptions{
		BasePath: filepath.Join(root, "audit.db"), StatePath: filepath.Join(root, "state.json"), CoordinationPath: filepath.Join(root, "catalog.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil || reopened.ID != latest.ID {
		t.Fatalf("changed retention = %+v, %v", reopened, err)
	}
	if _, err := os.Stat(old.Path); err != nil {
		t.Fatalf("retention edit removed history: %v", err)
	}
	status, err := catalog.Status(policy, now)
	if err != nil || status.CurrentBucketID != latest.ID || len(status.RetainedFiles) != 2 {
		t.Fatalf("reopened status = %+v, %v", status, err)
	}
}

func TestStatusCountsExpiredPinnedFilesAndOrphanedSidecars(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Retained = 1
	old, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	writeSizedIntake(t, catalog, old, "expired")
	reader, err := catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(filepath.Dir(old.Path), "audit-20261025T000000Z.db-journal")
	if err := os.WriteFile(orphan, []byte("interrupted deletion"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := catalog.Status(policy, now)
	if err != nil || len(status.PendingFiles) != 2 || status.TotalBytes < 1024*1024 {
		t.Fatalf("pending file accounting = %+v, %v", status, err)
	}
	if _, err := catalog.Prune(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned journal remains: %v", err)
	}
	status, err = catalog.Status(policy, now)
	if err != nil || len(status.PendingFiles) != 1 || len(status.DeferredFiles) != 1 {
		t.Fatalf("pinned expired accounting = %+v, %v", status, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Prune(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	status, err = catalog.Status(policy, now)
	if err != nil || len(status.PendingFiles) != 0 || status.TotalBytes >= 1024*1024 {
		t.Fatalf("after expiration = %+v, %v", status, err)
	}
}

func TestStatusCountsPreviousStorageFamilyDuringPendingReset(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 5 {
		writeSizedIntake(t, catalog, old, fmt.Sprintf("pending-reset-%d", index))
	}
	reader, err := catalog.OpenWriter(t.Context(), old)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	root := filepath.Dir(old.Path)
	replacement, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{
		BasePath:         filepath.Join(root, "new", "audit.db"),
		StatePath:        filepath.Join(root, "state.json"),
		CoordinationPath: filepath.Join(root, "catalog.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Reset(t.Context(), policy, now); !errors.Is(err, auditstorage.ErrResetPending) {
		t.Fatalf("reset with pinned previous storage = %v", err)
	}
	status, err := replacement.Status(policy, now)
	if err != nil || !status.ResetPending || status.ExcessBytes <= 0 || len(status.PendingFiles) != 1 || status.PendingFiles[0].Path != old.Path {
		t.Fatalf("previous storage accounting = %+v, %v", status, err)
	}
}

func TestIntakeQueryReadsAllSameDaySegmentsAndFiltersExactSegment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "runtime"))
	var cfg config.Config
	cfg.Audit.Outputs.SQLite.Path = filepath.Join(root, "audit.db")
	maxSize := "4MiB"
	cfg.Audit.Storage.MaxSize = &maxSize
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.AuditStoragePolicy().Rotation()
	first, err := catalog.EnsureCurrent(t.Context(), policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeSizedIntake(t, catalog, first, "first-segment")
	second, err := catalog.EnsureCurrent(t.Context(), policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	writeSizedIntake(t, catalog, second, "second-segment")
	result, err := intake.Query(t.Context(), &cfg, intake.QueryFilter{Limit: 10})
	if err != nil || len(result.Records) != 2 || result.Records[0].BucketID != second.ID || result.Records[1].BucketID != first.ID {
		t.Fatalf("global segment query = %+v, %v", result, err)
	}
	result, err = intake.Query(t.Context(), &cfg, intake.QueryFilter{BucketID: second.ID, Limit: 10})
	if err != nil || len(result.Records) != 1 || result.Records[0].EventID != "second-segment" {
		t.Fatalf("exact segment query = %+v, %v", result, err)
	}
}

func TestSelectedPruningDefersAdditionalFilesAfterConcurrentGrowth(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	var latest auditstorage.Bucket
	for index := range 5 {
		bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
		if err != nil {
			t.Fatal(err)
		}
		writeSizedIntake(t, catalog, bucket, fmt.Sprintf("selected-%d", index))
		latest = bucket
	}
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	selected, err := catalog.PruneCandidates(t.Context(), policy, now)
	if err != nil || len(selected) == 0 {
		t.Fatalf("selected cleanup = %v, %v", selected, err)
	}
	for index := range 5 {
		writeSizedIntake(t, catalog, latest, fmt.Sprintf("concurrent-growth-%d", index))
	}
	result, err := catalog.PruneSelected(t.Context(), policy, now, selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range result.Removed {
		if !slices.Contains(selected, path) {
			t.Fatalf("deleted a file before replay cancellation: %s", path)
		}
	}
	status, err := catalog.Status(policy, now)
	if err != nil || status.ExcessBytes <= 0 {
		t.Fatalf("new growth must wait for the next check: %+v, %v", status, err)
	}
	selected, err = catalog.PruneCandidates(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.PruneSelected(t.Context(), policy, now, selected); err != nil {
		t.Fatal(err)
	}
	status, err = catalog.Status(policy, now)
	if err != nil || status.ExcessBytes != 0 {
		t.Fatalf("next cleanup did not remove excess bytes: %+v, %v", status, err)
	}
}
