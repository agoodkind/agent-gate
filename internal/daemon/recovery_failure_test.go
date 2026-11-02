package daemon_test

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/intake"
)

func TestRecoveryRetriesHistoricalTraversalAfterCatalogRepair(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	historical := seedRecoveryBacklog(t, harness, 1)
	logPath := filepath.Join(t.TempDir(), "traversal.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewTextHandler(logFile, nil))
	originalLog := slog.Default()
	slog.SetDefault(log)
	defer slog.SetDefault(originalLog)
	server, err := daemon.New(t.Context(), log, harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	harness.server = server
	statePath := harness.cfg.AuditCatalogOptions().StatePath
	saved, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	server.StartAuditScheduler(t.Context())
	ownershipWait(t, func() bool {
		return strings.Contains(readRecoveryLog(t, logPath), "discover audit files failed")
	})
	failedAt := time.Now()
	// Keep the failure present across a scheduler tick to detect immediate retries.
	time.Sleep(1100 * time.Millisecond)
	if count := strings.Count(readRecoveryLog(t, logPath), "discover audit files failed"); count != 1 {
		t.Fatalf("persistent traversal failure retried %d times before its deadline", count)
	}
	if err := os.WriteFile(statePath, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	harness.accept(t, "current-after-repair")
	current := harness.nextCall(t)
	if !strings.Contains(current.input, "current-after-repair") {
		t.Fatalf("unexpected inference before retry: %s", current.input)
	}
	close(current.release)
	ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg)[1] == 1 })
	// Allow fifteen seconds for dispatch beyond the thirty-second retry delay.
	timer := time.NewTimer(time.Until(failedAt.Add(45 * time.Second)))
	defer timer.Stop()
	select {
	case recovered := <-harness.service.entered:
		if !strings.Contains(recovered.input, "backlog-0") {
			t.Fatalf("unexpected retry inference: %s", recovered.input)
		}
		close(recovered.release)
		ownershipWait(t, func() bool { return ownershipOutcomes(t, harness.cfg) == [4]int{2, 2, 2, 2} })
		t.Logf("historical receipt completed after %s; current receipt completed before retry", time.Since(failedAt))
	case <-timer.C:
		handle, err := ownershipCatalog(t, harness.cfg).OpenWriter(t.Context(), historical)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = handle.Close() }()
		store, err := intake.NewStore(t.Context(), handle.Database, harness.cfg.AuditStoragePolicy(), ownershipLogger())
		if err != nil {
			t.Fatal(err)
		}
		pending, err := store.HasPending(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("historical retry did not run; durable_pending=%v; log=%s", pending, readRecoveryLog(t, logPath))
	}
}

func readRecoveryLog(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

type corruptRecoveryLog struct {
	messages chan string
	resume   <-chan struct{}
}

func (writer corruptRecoveryLog) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "recover audit file failed") {
		select {
		case writer.messages <- string(body):
			<-writer.resume
		default:
		}
	}
	return len(body), nil
}

func TestRecoveryContinuesAfterCorruptHistoricalFile(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	catalog := ownershipCatalog(t, harness.cfg)
	historical := make(map[string]auditstorage.Bucket)
	for day := 2; day >= 1; day-- {
		bucket, err := catalog.EnsureCurrent(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-time.Duration(day)*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		handle, err := catalog.OpenWriter(t.Context(), bucket)
		if err != nil {
			t.Fatal(err)
		}
		store, err := intake.NewStore(t.Context(), handle.Database, harness.cfg.AuditStoragePolicy(), ownershipLogger())
		if err != nil {
			t.Fatal(err)
		}
		identifier := "historical-" + bucket.ID
		payload := ownershipPayload(identifier)
		receipt, err := store.Append(t.Context(), intake.Record{EventID: identifier, System: "codex", SessionID: identifier, EventName: "PreToolUse", RawPayload: payload, NormalizedJSON: payload})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkDeferredPending(t.Context(), receipt.EventID, receipt.ReceiptID); err != nil {
			t.Fatal(err)
		}
		if err := handle.Close(); err != nil {
			t.Fatal(err)
		}
		historical[bucket.Path] = bucket
	}
	messages := make(chan string, 1)
	resume := make(chan struct{})
	releaseDiagnostic := sync.OnceFunc(func() { close(resume) })
	server, err := daemon.New(t.Context(), slog.New(slog.NewTextHandler(corruptRecoveryLog{messages: messages, resume: resume}, nil)), harness.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	defer releaseDiagnostic()
	harness.server = server
	order := recoveryHistoryOrder(t, catalog, harness, historical)
	corrupt, healthy := order[0], order[1]
	handle, err := catalog.OpenWriter(t.Context(), healthy)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	corruptBytes := []byte("corrupt retained history")
	if err := os.WriteFile(corrupt.Path, corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	server.StartAuditScheduler(t.Context())
	select {
	case message := <-messages:
		if !strings.Contains(message, corrupt.ID) {
			t.Fatalf("diagnostic omitted corrupt bucket: %s", message)
		}
	case call := <-harness.service.entered:
		close(call.release)
		t.Fatalf("healthy inference preceded the corrupt-file diagnostic: %s", call.input)
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not report the first corrupt file")
	}
	releaseDiagnostic()
	call := harness.nextCall(t)
	if !strings.Contains(call.input, healthy.ID) {
		t.Fatalf("recovery selected unexpected history: %s", call.input)
	}
	close(call.release)
	ownershipWait(t, func() bool {
		var counts [4]int
		if err := handle.Database.QueryRowContext(t.Context(), ownershipFixture(t, "ownership_outcome.sql")).Scan(&counts[0], &counts[1], &counts[2], &counts[3]); err != nil {
			t.Fatal(err)
		}
		return counts == [4]int{1, 1, 1, 1}
	})
	if body, err := os.ReadFile(corrupt.Path); err != nil || string(body) != string(corruptBytes) {
		t.Fatalf("corrupt file changed during unrelated recovery: %q: %v", body, err)
	}
	t.Logf("corrupt bucket %s failed before healthy bucket %s completed", corrupt.ID, healthy.ID)
}

func recoveryHistoryOrder(t *testing.T, catalog *auditstorage.Catalog, harness *ownershipHarness, historical map[string]auditstorage.Bucket) []auditstorage.Bucket {
	t.Helper()
	files, err := catalog.Recovery(t.Context(), harness.cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = files.Close() }()
	var order []auditstorage.Bucket
	for {
		buckets, err := files.Next()
		if err != nil && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
		for _, bucket := range buckets {
			if _, exists := historical[bucket.Path]; exists {
				order = append(order, bucket)
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if len(order) != 2 {
		t.Fatalf("historical recovery order contains %d files", len(order))
	}
	return order
}
