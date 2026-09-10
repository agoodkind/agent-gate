package auditstorage_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestStatusUsesMetadataWhileRealWritesContinue(t *testing.T) {
	root := t.TempDir()
	catalog, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{BasePath: filepath.Join(root, "data", "audit.db"), StatePath: filepath.Join(root, "policy.json"), CoordinationPath: filepath.Join(root, "catalog.lock")})
	if err != nil {
		t.Fatal(err)
	}
	policy := auditstorage.RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var count atomic.Int64
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for index := 0; ; index++ {
			if ctx.Err() != nil {
				done <- nil
				return
			}
			if err := audit.WriteEvents(t.Context(), handle.Database, []audit.Event{{EventID: fmt.Sprintf("write-%d", index), Time: now.Format(time.RFC3339Nano)}}); err != nil {
				done <- err
				return
			}
			count.Add(1)
			if index == 0 {
				close(ready)
			}
		}
	}()
	<-ready
	for range 50 {
		status, err := catalog.Status(policy, now)
		if err != nil {
			t.Fatal(err)
		}
		if status.CurrentBucketID != bucket.ID || status.RetentionBuckets != 7 || len(status.RetainedFiles) != 1 || status.TotalBytes <= 0 {
			t.Fatalf("status = %+v", status)
		}
		if !status.NextBoundary.Equal(now.Truncate(24 * time.Hour).Add(24 * time.Hour)) {
			t.Fatalf("boundary = %s", status.NextBoundary)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if count.Load() < 2 {
		t.Fatal("writes did not continue during status calls")
	}
	files, err := os.ReadDir(filepath.Dir(bucket.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() != filepath.Base(bucket.Path) && file.Name() != filepath.Base(bucket.Path)+"-wal" && file.Name() != filepath.Base(bucket.Path)+"-shm" {
			t.Fatalf("status created history copy or database: %s", file.Name())
		}
	}
}
