package auditstorage_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestBoundedPruningFindsFutureFileBeyondRetainedPages(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Interval = time.Hour
	policy.Retained = 200
	policy.MaxBytes = 0
	var oldest auditstorage.Bucket

	for index := range 120 {
		bucket, err := catalog.EnsureCurrent(
			t.Context(),
			policy,
			now.Add(time.Duration(index-120)*time.Hour),
		)
		if err != nil {
			t.Fatal(err)
		}

		if index == 0 {
			oldest = bucket
		}
	}

	future, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}

	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil || len(targets) != 1 || targets[0].Path != future.Path {
		t.Fatalf("future candidate after retained pages = %+v: %v", targets, err)
	}

	reader, err := catalog.OpenWriter(t.Context(), oldest)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reader.Close() }()
	policy.MaxBytes = 4 * 1024 * 1024
	targets, err = catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil || len(targets) != 2 || targets[0].Path != oldest.Path ||
		targets[1].Path != future.Path {
		t.Fatalf("blocked chronological candidates = %+v: %v", targets, err)
	}

	result, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets)
	if err != nil || len(result.Removed) != 1 || result.Removed[0] != future.Path ||
		len(result.Deferred) != 1 ||
		result.Deferred[0] != oldest.Path {
		t.Fatalf("blocked size and expired future = %+v: %v", result, err)
	}

	if _, err := os.Stat(oldest.Path); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(future.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("future database survived: %v", err)
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	for {
		targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
		if err != nil {
			t.Fatal(err)
		}

		if len(targets) == 0 {
			break
		}

		if len(targets) > 100 {
			t.Fatalf("pruning retained %d candidates", len(targets))
		}

		result, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets)
		if err != nil || len(result.Removed) == 0 {
			t.Fatalf("pruning failed to advance: %+v: %v", result, err)
		}
	}
}

func TestPruningRechecksSelectedFileIdentity(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Retained = 1
	old, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}

	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil || len(targets) != 1 {
		t.Fatalf("selected target = %+v: %v", targets, err)
	}

	if err := os.Rename(old.Path, old.Path+".saved"); err != nil {
		t.Fatal(err)
	}

	database, err := auditstorage.OpenWriter(t.Context(), old.Path)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	result, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets)
	if err != nil || len(result.Removed) != 0 {
		t.Fatalf("replaced inode deletion = %+v: %v", result, err)
	}

	if _, err := os.Stat(old.Path); err != nil {
		t.Fatal(err)
	}
}
