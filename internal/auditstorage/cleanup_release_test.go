package auditstorage_test

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

type cleanupReleaseObserver struct {
	once     sync.Once
	reader   *auditstorage.BucketHandle
	released chan error
}

func (observer *cleanupReleaseObserver) Write(data []byte) (int, error) {
	if strings.Contains(string(data), "counted blocked audit files") {
		observer.once.Do(func() { observer.released <- observer.reader.Close() })
	}
	return len(data), nil
}

func TestCleanupStatusDetectsReaderReleaseBetweenComparisonScans(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Interval = time.Hour
	policy.Retained = 1
	policy.MaxBytes = 0
	before, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	beforeReader, err := catalog.OpenWriter(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = beforeReader.Close() }()
	future, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	futureReader, err := catalog.OpenWriter(t.Context(), future)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = futureReader.Close() }()
	if _, err := catalog.Prune(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	status, err := catalog.Status(policy, now)
	if err != nil || len(status.DeferredFiles) != 1 || status.DeferredFiles[0] != future.Path {
		t.Fatalf("initial deferred status = %v: %v", status.DeferredFiles, err)
	}
	later := now.Add(time.Hour)
	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, later)
	if err != nil || len(targets) != 1 || targets[0].Path != before.Path {
		t.Fatalf("selected old file = %+v: %v", targets, err)
	}
	observer := &cleanupReleaseObserver{reader: futureReader, released: make(chan error, 1)}
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(observer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(original)
	if _, err := catalog.PruneBatchSelected(t.Context(), policy, later, targets); err != nil {
		t.Fatal(err)
	}
	slog.SetDefault(original)
	select {
	case err := <-observer.released:
		if err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatal("reader did not release during comparison")
	}
	status, err = catalog.Status(policy, later)
	if err != nil || len(status.DeferredFiles) != 1 || status.DeferredFiles[0] != before.Path {
		t.Fatalf("replacement deferred status = %v: %v", status.DeferredFiles, err)
	}
}
