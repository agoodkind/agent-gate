package auditstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	directory := t.TempDir()
	catalog, err := NewCatalog(CatalogOptions{BasePath: filepath.Join(directory, "audit.db"), StatePath: filepath.Join(directory, "state.json"), CoordinationPath: filepath.Join(directory, "coordination.lock")})
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestExpiredFilesDoNotSurviveDowntime(t *testing.T) {
	directory := t.TempDir()
	catalog, err := NewCatalog(CatalogOptions{
		BasePath:         filepath.Join(directory, "audit.db"),
		StatePath:        filepath.Join(directory, "state.json"),
		CoordinationPath: filepath.Join(directory, "coordination.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	start := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	old, err := catalog.EnsureCurrent(t.Context(), policy, start)
	if err != nil {
		t.Fatal(err)
	}
	later := start.Add(30 * 24 * time.Hour)
	if _, err := catalog.EnsureCurrent(t.Context(), policy, later); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.Prune(t.Context(), policy, later); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired database remains: %v", err)
	}
}

func TestCatalogResetInterruption(t *testing.T) {
	for _, operation := range []string{"state", "delete", "create"} {
		t.Run(operation, func(t *testing.T) {
			catalog := testCatalog(t)
			policy := RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
			now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			old, err := catalog.EnsureCurrent(t.Context(), policy, now)
			if err != nil {
				t.Fatal(err)
			}
			failure := errors.New("injected interruption")
			catalog.filesystemCheckpoint = func(step string, path string) error {
				if step == operation {
					return failure
				}
				return nil
			}
			changed := RotationPolicy{Interval: policy.Interval, Retained: 3}
			if _, err := catalog.Reset(t.Context(), changed, now); !errors.Is(err, failure) {
				t.Fatalf("reset = %v", err)
			}
			restarted, err := NewCatalog(catalog.options)
			if err != nil {
				t.Fatal(err)
			}
			state, err := restarted.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if operation == "state" {
				if state.ResetPending {
					t.Fatal("failed state write changed active policy")
				}
				if _, err := os.Stat(old.Path); err != nil {
					t.Fatalf("history changed before pending write: %v", err)
				}
			} else {
				if !state.ResetPending {
					t.Fatal("interrupted reset published ready")
				}
				if _, err := restarted.EnsureCurrent(t.Context(), policy, now); !errors.Is(err, ErrResetPending) {
					t.Fatalf("reverted config reopened pending history: %v", err)
				}
				if _, err := restarted.Read(t.Context(), changed, now); !errors.Is(err, ErrResetPending) {
					t.Fatalf("read pending history: %v", err)
				}
				if _, err := restarted.OpenWriter(t.Context(), old); !errors.Is(err, ErrResetPending) {
					t.Fatalf("writer pending history: %v", err)
				}
			}
			if _, err := restarted.Reset(t.Context(), policy, now); err != nil {
				t.Fatal(err)
			}
			state, err = restarted.loadState()
			if err != nil {
				t.Fatal(err)
			}
			if state.ResetPending || len(state.PendingBasePaths) != 0 || state.Retained != 7 {
				t.Fatalf("reset state = %+v", state)
			}
		})
	}
}

func TestPendingFamiliesSurviveFurtherPathChanges(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	first := catalog.options.BasePath
	second := filepath.Join(filepath.Dir(first), "second.db")
	third := filepath.Join(filepath.Dir(first), "third.db")
	for _, base := range []string{second, third} {
		options := catalog.options
		options.BasePath = base
		catalog, err = NewCatalog(options)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err == nil {
			t.Fatal("changed path opened history")
		}
		catalog.filesystemCheckpoint = func(operation string, path string) error {
			if operation == "delete" {
				return errors.New("interrupted")
			}
			return nil
		}
		if _, err := catalog.Reset(t.Context(), policy, now); err == nil {
			t.Fatal("reset did not interrupt")
		}
	}
	state, err := catalog.loadState()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.PendingBasePaths, []string{first, second, third}) {
		t.Fatalf("pending paths = %v", state.PendingBasePaths)
	}
	options := catalog.options
	options.BasePath = first
	restarted, err := NewCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reset(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original family remains: %v", err)
	}
}

func TestReadersAndWritersDeferPruneAndBlockReset(t *testing.T) {
	for _, readonly := range []bool{true, false} {
		t.Run(fmtBool(readonly), func(t *testing.T) {
			catalog := testCatalog(t)
			policy := RotationPolicy{Interval: time.Hour, Retained: 1}
			now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
			if err != nil {
				t.Fatal(err)
			}
			var handle *BucketHandle
			if readonly {
				set, err := catalog.Read(t.Context(), policy, now)
				if err != nil {
					t.Fatal(err)
				}
				handle = set.Handles[0]
				query, err := os.ReadFile("testdata/catalog_write_probe.sql")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := handle.Database.ExecContext(t.Context(), string(query)); err == nil || !strings.Contains(err.Error(), "readonly") {
					t.Fatalf("read-only write = %v", err)
				}
			} else {
				handle, err = catalog.OpenWriter(t.Context(), bucket)
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = handle.Close() })
			other, err := NewCatalog(catalog.options)
			if err != nil {
				t.Fatal(err)
			}
			result, err := other.Prune(t.Context(), policy, now.Add(2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Removed) != 0 || !reflect.DeepEqual(result.Deferred, []string{bucket.Path}) {
				t.Fatalf("prune = %+v", result)
			}
			if _, err := other.Reset(t.Context(), policy, now); !errors.Is(err, ErrResetPending) {
				t.Fatalf("reset while in use = %v", err)
			}
			if err := handle.Close(); err != nil {
				t.Fatal(err)
			}
			if err := handle.Database.PingContext(t.Context()); err == nil {
				t.Fatal("handle left SQLite open")
			}
			if _, err := other.Reset(t.Context(), policy, now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fmtBool(value bool) string {
	if value {
		return "reader"
	}
	return "writer"
}

func TestReadExcludesMissingMisalignedAndFutureWindows(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: 24 * time.Hour, Retained: 3}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, offset := range []int{-10, -2, 0, 1} {
		if _, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(time.Duration(offset)*24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	misaligned := strings.TrimSuffix(catalog.options.BasePath, ".db") + "-20260907T010000Z.db"
	if err := os.WriteFile(misaligned, []byte("unrelated content"), 0o600); err != nil {
		t.Fatal(err)
	}
	set, err := catalog.Read(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = set.Close() }()
	if len(set.Handles) != 2 {
		t.Fatalf("read count = %d, want only current and two days ago", len(set.Handles))
	}
	if set.Handles[0].Bucket.ID != "20260906T000000Z" || set.Handles[1].Bucket.ID != "20260908T000000Z" {
		t.Fatalf("read windows = %+v", set.Handles)
	}
}

func TestPurgePreservesUnrelatedFilesSymlinksAndCoordination(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 1}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	coordination, err := os.Stat(catalog.options.CoordinationPath)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(filepath.Dir(bucket.Path), "other-20260908T120000Z.db")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := strings.Replace(bucket.Path, "120000", "110000", 1)
	if err := os.Symlink(unrelated, symlink); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Purge(t.Context(), func([]string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bucket.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("database remains: %v", err)
	}
	for _, path := range []string{unrelated, symlink} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("unrelated path changed: %v", err)
		}
	}
	after, err := os.Stat(catalog.options.CoordinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(coordination, after) {
		t.Fatal("coordination inode changed")
	}
	if _, err := os.Stat(catalog.options.StatePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("purge left policy metadata: %v", err)
	}
}

func TestCatalogCoordinationRespectsCancellation(t *testing.T) {
	catalog := testCatalog(t)
	lock, err := catalog.coordinate(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = catalog.Read(ctx, RotationPolicy{Interval: time.Hour, Retained: 1}, time.Now())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended coordination = %v", err)
	}
}

func TestResetRetriesOrphanedSidecarDeletion(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 1}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bucket.Path+"-wal", []byte("interrupted sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.filesystemCheckpoint = func(operation string, path string) error {
		if operation == "delete" && strings.HasSuffix(path, "-wal") {
			return errors.New("interrupted after database removal")
		}
		return nil
	}
	if _, err := catalog.Reset(t.Context(), policy, now.Add(time.Hour)); err == nil {
		t.Fatal("reset did not interrupt")
	}
	restarted, err := NewCatalog(catalog.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Reset(t.Context(), policy, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bucket.Path + "-wal"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned sidecar remains: %v", err)
	}
}

func TestOrdinaryBucketCreationPublishesOnlyInitializedDatabase(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Hour)
	desired := catalog.current(policy, later)
	catalog.filesystemCheckpoint = func(operation string, path string) error {
		if operation == "publish" {
			return errors.New("interrupted before publication")
		}
		return nil
	}
	if _, err := catalog.EnsureCurrent(t.Context(), policy, later); err == nil {
		t.Fatal("creation did not interrupt")
	}
	if _, err := os.Stat(desired.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial final file became visible: %v", err)
	}
	if _, err := os.Stat(old.Path); err != nil {
		t.Fatalf("rollover destroyed older history: %v", err)
	}
	restarted, err := NewCatalog(catalog.options)
	if err != nil {
		t.Fatal(err)
	}
	// A crash can leave an incomplete staging database and a sidecar.
	if err := os.WriteFile(desired.Path+".creating", []byte("partial database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desired.Path+".creating-wal", []byte("partial WAL"), 0o600); err != nil {
		t.Fatal(err)
	}
	bucket, err := restarted.EnsureCurrent(t.Context(), policy, later.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := restarted.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	query, err := os.ReadFile("testdata/catalog_event_count.sql")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := handle.Database.QueryRowContext(t.Context(), string(query), "missing").Scan(&count); err != nil {
		t.Fatalf("published incomplete schema: %v", err)
	}
	for _, suffix := range []string{".creating", ".creating-wal"} {
		if _, err := os.Stat(desired.Path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging file remains: %v", err)
		}
	}
}

func TestResetReadyPublicationFailureRemainsPending(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	writes := 0
	catalog.filesystemCheckpoint = func(operation string, path string) error {
		if operation == "state" {
			writes++
			if writes == 2 {
				return errors.New("ready publication interrupted")
			}
		}
		return nil
	}
	if _, err := catalog.Reset(t.Context(), policy, now); err == nil {
		t.Fatal("ready publication did not fail")
	}
	restarted, err := NewCatalog(catalog.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.EnsureCurrent(t.Context(), policy, now); !errors.Is(err, ErrResetPending) {
		t.Fatalf("opened replacement before ready publication: %v", err)
	}
	if _, err := restarted.Reset(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeDeletesStagingArtifacts(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".creating", ".creating-shm", ".creating-wal"} {
		if err := os.WriteFile(bucket.Path+suffix, []byte("interrupted staging"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := catalog.Purge(t.Context(), func([]string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".creating", ".creating-shm", ".creating-wal"} {
		if _, err := os.Stat(bucket.Path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging remains: %v", err)
		}
	}
}

func TestCatalogCanonicalPathAndLiteralStem(t *testing.T) {
	directory := t.TempDir()
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(directory, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}
	options := CatalogOptions{BasePath: filepath.Join(alias, "audit[*].sqlite"), StatePath: filepath.Join(directory, "state.json"), CoordinationPath: filepath.Join(directory, "coordination.lock")}
	catalog, err := NewCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	policy := RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	options.BasePath = filepath.Join(realDirectory, "audit[*].sqlite")
	equivalent, err := NewCatalog(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := equivalent.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatalf("canonical spelling triggered policy change: %v", err)
	}
	unrelated := filepath.Join(realDirectory, "auditX-20260908T000000Z.sqlite")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := equivalent.Purge(t.Context(), func([]string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bucket.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("literal family remains: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("glob-like stem removed unrelated file: %v", err)
	}
}

func TestConcurrentReadersReleaseAllPruneLocks(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 2}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, instant := range []time.Time{now.Add(-time.Hour), now} {
		if _, err := catalog.EnsureCurrent(t.Context(), policy, instant); err != nil {
			t.Fatal(err)
		}
	}
	const readers = 4
	ready := make(chan error, readers)
	closeReaders := make(chan struct{})
	closed := make(chan error, readers)
	for range readers {
		go func() {
			set, err := catalog.Read(t.Context(), policy, now)
			ready <- err
			if err != nil {
				return
			}
			<-closeReaders
			closed <- set.Close()
		}()
	}
	for range readers {
		if err := <-ready; err != nil {
			t.Fatal(err)
		}
	}
	result, err := catalog.Prune(t.Context(), policy, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deferred) != 2 {
		t.Fatalf("prune while read sets open = %+v", result)
	}
	close(closeReaders)
	for range readers {
		if err := <-closed; err != nil {
			t.Fatal(err)
		}
	}
	result, err = catalog.Prune(t.Context(), policy, now.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 2 || len(result.Deferred) != 0 {
		t.Fatalf("prune after read sets close = %+v", result)
	}
}

func TestPruneRetriesOrphanedSidecarDeletion(t *testing.T) {
	catalog := testCatalog(t)
	policy := RotationPolicy{Interval: time.Hour, Retained: 1}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bucket.Path+"-shm", []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog.filesystemCheckpoint = func(operation string, path string) error {
		if operation == "delete" && strings.HasSuffix(path, "-shm") {
			return errors.New("interrupted prune")
		}
		return nil
	}
	if _, err := catalog.Prune(t.Context(), policy, now.Add(time.Hour)); err == nil {
		t.Fatal("prune did not interrupt")
	}
	restarted, err := NewCatalog(catalog.options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Prune(t.Context(), policy, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bucket.Path + "-shm"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan remains: %v", err)
	}
}

func TestCatalogSupportsExtensionsThatResembleSidecars(t *testing.T) {
	for _, name := range []string{"audit.creating", "audit.db-wal", "audit"} {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalog(t)
			options := catalog.options
			options.BasePath = filepath.Join(filepath.Dir(options.BasePath), name)
			catalog, err := NewCatalog(options)
			if err != nil {
				t.Fatal(err)
			}
			policy := RotationPolicy{Interval: time.Hour, Retained: 1}
			now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
			bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(bucket.Path); err != nil {
				t.Fatalf("creation cleanup removed current database: %v", err)
			}
			if err := catalog.Purge(t.Context(), func([]string) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(bucket.Path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unrecognized extension survived purge: %v", err)
			}
		})
	}
}
