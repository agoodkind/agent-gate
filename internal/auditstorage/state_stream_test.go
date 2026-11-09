package auditstorage_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestRotationPreservesLegacyDeferredMetadata(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	bucket, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(filepath.Dir(bucket.Path), "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	var state auditstorage.CatalogState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}

	for index := range 1001 {
		state.DeferredFiles = append(state.DeferredFiles, fmt.Sprintf("legacy-deferred-%d", index))
	}

	state.CleanupError = "legacy reader still owns audit files"
	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	status, err := catalog.Status(policy, now.Add(24*time.Hour))
	if err != nil || status.CleanupError != state.CleanupError ||
		!slices.Equal(status.DeferredFiles, state.DeferredFiles) {
		t.Fatalf(
			"legacy cleanup metadata changed: %d paths, %s: %v",
			len(status.DeferredFiles),
			status.CleanupError,
			err,
		)
	}
}

func TestPendingResetStreamsEveryLegacyFamily(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	first, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	reader, err := catalog.OpenWriter(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = reader.Close() }()
	root := filepath.Dir(first.Path)
	statePath := filepath.Join(root, "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	var state auditstorage.CatalogState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}

	state.ResetPending = true
	state.PendingBasePaths = append(state.PendingBasePaths, state.BasePath)

	for index := range 120 {
		path := filepath.Join(root, fmt.Sprintf("family-%03d.db", index))
		database, err := auditstorage.OpenWriter(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}

		if err := database.Close(); err != nil {
			t.Fatal(err)
		}

		state.PendingBasePaths = append(state.PendingBasePaths, path)
	}

	data, err = json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.Reset(t.Context(), policy, now.Add(24*time.Hour)); !errors.Is(
		err,
		auditstorage.ErrResetPending,
	) {
		t.Fatalf("reader did not defer reset: %v", err)
	}

	data, err = os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}

	var pending auditstorage.CatalogState
	if err := json.Unmarshal(data, &pending); err != nil {
		t.Fatal(err)
	}

	for _, path := range state.PendingBasePaths {
		if !slices.Contains(pending.PendingBasePaths, path) {
			t.Fatalf("pending reset lost %s", path)
		}
	}

	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := catalog.Reset(t.Context(), policy, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	for _, path := range state.PendingBasePaths {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("legacy family survived reset: %s: %v", path, err)
		}
	}

	if _, err := os.Stat(first.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original bucket survived reset: %v", err)
	}
}
