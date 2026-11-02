package auditstorage_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

type cleanupSortRecord struct {
	Message        string `json:"msg"`
	Paths          int    `json:"paths"`
	Passes         int    `json:"passes"`
	TemporaryBytes int64  `json:"temporary_bytes"`
}

type cleanupSortObserver struct {
	cancel  context.CancelFunc
	records chan cleanupSortRecord
}

func (observer cleanupSortObserver) Write(data []byte) (int, error) {
	var record cleanupSortRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return 0, err
	}
	if record.Message == "sorted deferred metadata" {
		observer.records <- record
		observer.cancel()
	}
	return len(data), nil
}

func TestCleanupSortCancelsEmissionAndRemovesInterruptedArtifacts(t *testing.T) {
	for _, count := range []int{100, 205} {
		t.Run(fmt.Sprint(count), func(t *testing.T) { exerciseCleanupSort(t, count) })
	}
}

func exerciseCleanupSort(t *testing.T, blockedFiles int) {
	t.Helper()
	catalog, policy, now := sizeCatalog(t)
	policy.Interval = time.Hour
	policy.Retained = 300
	policy.MaxBytes = 0
	var first auditstorage.Bucket
	want := make([]string, 0, blockedFiles)
	for index := range blockedFiles {
		offset := index*73%blockedFiles - 250
		bucket, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(time.Duration(offset)*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = bucket
		}
		want = append(want, bucket.Path)
		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
	}
	slices.Sort(want)
	if _, err := catalog.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	policy.Retained = 1
	targets, err := catalog.PruneBatchCandidates(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(filepath.Dir(first.Path), "state.json")
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	records := make(chan cleanupSortRecord, 1)
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(cleanupSortObserver{cancel: cancel, records: records}, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(original)
	_, err = catalog.PruneBatchSelected(ctx, policy, now, targets)
	slog.SetDefault(original)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("final emission cancellation = %v", err)
	}
	select {
	case record := <-records:
		if record.Paths != blockedFiles {
			t.Fatalf("sort accounting = %+v", record)
		}
		if blockedFiles <= 100 && record.TemporaryBytes != 0 {
			t.Fatalf("bounded in-memory sort wrote %d temporary bytes", record.TemporaryBytes)
		}
		t.Logf("blocked_paths=%d passes=%d temporary_written_bytes=%d", record.Paths, record.Passes, record.TemporaryBytes)
	default:
		t.Fatal("sort did not finish before cancellation")
	}
	after, err := os.ReadFile(statePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("canceled sort published partial state: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(statePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".audit-deferred-sort-") {
			t.Fatalf("sort left a named temporary file: %s", entry.Name())
		}
	}
	if _, err := catalog.PruneBatchSelected(t.Context(), policy, now, targets); err != nil {
		t.Fatal(err)
	}
	status, err := catalog.Status(policy, now)
	if err != nil || !slices.Equal(status.DeferredFiles, want) {
		t.Fatalf("complete sorted status = %d: %v", len(status.DeferredFiles), err)
	}
	digest := sha256.Sum256([]byte(statePath))
	prefix := fmt.Sprintf(".audit-deferred-sort-%x-", digest[:8])
	interrupted := filepath.Join(filepath.Dir(statePath), prefix+"interrupted.tmp")
	unrelated := filepath.Join(filepath.Dir(statePath), prefix+"unrelated.tmp")
	if err := os.WriteFile(interrupted, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unrelated, []byte("unrelated data"), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted, err := auditstorage.NewCatalog(auditstorage.CatalogOptions{BasePath: filepath.Join(filepath.Dir(statePath), "audit.db"), StatePath: statePath, CoordinationPath: filepath.Join(filepath.Dir(statePath), "catalog.lock")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.EnsureCurrent(t.Context(), policy, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(interrupted); !os.IsNotExist(err) {
		t.Fatalf("interrupted creation survived restart: %v", err)
	}
	if body, err := os.ReadFile(unrelated); err != nil || string(body) != "unrelated data" {
		t.Fatalf("restart changed unrelated data: %q: %v", body, err)
	}
}
