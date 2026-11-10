package auditstorage_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryDetectsMissingDirectoryAtTraversalEnd(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	pass, err := catalog.Recovery(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pass.Close() }()
	if _, err := pass.Next(); err != nil {
		t.Fatal(err)
	}

	directory := filepath.Dir(bucket.Path)
	unavailable := directory + "-unavailable"
	if err := os.Rename(directory, unavailable); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if _, err := os.Stat(unavailable); err == nil {
			if err := os.Rename(unavailable, directory); err != nil {
				t.Error(err)
			}
		}
	}()
	if _, err := pass.Next(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("traversal error = %v, want missing directory", err)
	}

	if err := os.Rename(unavailable, directory); err != nil {
		t.Fatal(err)
	}

	retry, err := catalog.Recovery(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = retry.Close() }()
	buckets, err := retry.Next()
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	for _, recovered := range buckets {
		if recovered.Path == bucket.Path {
			return
		}
	}

	t.Fatalf("restored bucket %s was not discovered", bucket.Path)
}

func TestRecoveryFreshPassSkipsDeletedBucket(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	pass, err := catalog.Recovery(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(bucket.Path); err != nil {
		t.Fatal(err)
	}

	if err := pass.Close(); err != nil {
		t.Fatal(err)
	}

	retry, err := catalog.Recovery(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = retry.Close() }()

	for {
		buckets, err := retry.Next()
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}

		for _, recovered := range buckets {
			if recovered.Path == bucket.Path {
				t.Fatalf("fresh traversal returned deleted bucket %s", bucket.Path)
			}
		}

		if errors.Is(err, io.EOF) {
			return
		}
	}
}
