package auditstorage_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/intake"
)

func TestPruningProgressAndCompleteStatusBeyondOneHundredReaders(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Interval = time.Hour
	policy.Retained = 200
	policy.MaxBytes = 0
	var readers []*auditstorage.BucketHandle
	var blocked []string
	var available auditstorage.Bucket

	for index := range 121 {
		bucket, err := catalog.EnsureCurrent(
			t.Context(),
			policy,
			now.Add(time.Duration(index-140)*time.Hour),
		)
		if err != nil {
			t.Fatal(err)
		}

		if index == 120 {
			available = bucket
			continue
		}

		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}

		readers = append(readers, handle)
		blocked = append(blocked, bucket.Path)
		defer func() { _ = handle.Close() }()
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}

	policy.Retained = 1
	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	if len(targets) > 100 {
		t.Fatalf("candidate batch = %d", len(targets))
	}

	result, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets)
	if err != nil || len(result.Removed) != 1 || result.Removed[0] != available.Path {
		t.Fatalf("available file behind readers = %+v: %v", result, err)
	}

	status, err := catalog.Status(policy, now)
	if err != nil || !slices.Equal(status.DeferredFiles, blocked) {
		t.Fatalf("complete blocked-file status = %d: %v", len(status.DeferredFiles), err)
	}

	statePath := filepath.Join(filepath.Dir(available.Path), "state.json")
	before, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}

	targets, err = catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(statePath)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("unchanged cleanup rewrote catalog state: %v", err)
	}

	for _, reader := range readers {
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPruningOrphanSelectionRejectsNewPrimary(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	policy.Retained = 1
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(bucket.Path); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(bucket.Path+"-wal", []byte("orphaned WAL"), 0o600); err != nil {
		t.Fatal(err)
	}

	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil || len(targets) != 1 {
		t.Fatalf("orphan selection = %+v: %v", targets, err)
	}

	replacementPath := filepath.Join(filepath.Dir(bucket.Path), "replacement.db")
	store, err := openFixtureIntake(t, t.Context(), replacementPath, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.Append(
		t.Context(),
		intake.Record{
			EventID:        "replacement",
			RawPayload:     []byte(`{}`),
			NormalizedJSON: []byte(`{}`),
		},
	); err != nil {
		t.Fatal(err)
	}

	if err := store.Handle().Close(); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(replacementPath, bucket.Path); err != nil {
		t.Fatal(err)
	}

	result, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets)
	if err != nil || len(result.Removed) != 0 {
		t.Fatalf("orphan selection deleted new primary: %+v: %v", result, err)
	}

	if _, err := os.Stat(bucket.Path); err != nil {
		t.Fatal(err)
	}

	store, err = openFixtureIntake(t, t.Context(), bucket.Path, nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Handle().Close() }()
	if _, err := store.Get(t.Context(), "replacement"); err != nil {
		t.Fatalf("replacement receipt did not survive: %v", err)
	}
}
