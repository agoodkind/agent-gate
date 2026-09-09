package auditstorage_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/intake"
)

func TestPolicyResetDiscardsCurrentFilenameHistory(t *testing.T) {
	directory := t.TempDir()
	catalog, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{
		BasePath: filepath.Join(directory, "audit.db"), StatePath: filepath.Join(directory, "state.json"), CoordinationPath: filepath.Join(directory, "coordination.lock"),
	})
	if err != nil {
		t.Fatal(err)
	}
	policy := auditstorage.RotationPolicy{Interval: 24 * time.Hour, Retained: 7}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	old, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	current, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openFixtureIntake(t, t.Context(), current.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(t.Context(), intake.Record{EventID: "before-reset", System: "codex", SessionID: "session", EventName: "PreToolUse", RawPayload: []byte(`{}`), NormalizedJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Handle().Close(); err != nil {
		t.Fatal(err)
	}
	policy.Retained = 3
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); !errors.Is(err, auditstorage.ErrPolicyChanged) {
		t.Fatalf("policy change = %v", err)
	}
	replacement, err := catalog.Reset(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.Path != current.Path {
		t.Fatalf("replacement path = %s, want %s", replacement.Path, current.Path)
	}
	if _, err := os.Stat(old.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old bucket remains: %v", err)
	}
	store, err = openFixtureIntake(t, t.Context(), replacement.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Handle().Close() }()
	query, err := os.ReadFile("testdata/catalog_event_count.sql")
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.Handle().QueryRowContext(t.Context(), string(query), "before-reset").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("old event remains: %d", count)
	}
}
