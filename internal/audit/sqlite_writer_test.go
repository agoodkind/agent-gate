package audit

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func testAuditDatabase(t *testing.T) *sql.DB {
	t.Helper()
	CaptureCancellationForTest(t)
	database, err := auditstorage.OpenWriter(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func testAuditEvent(id string) Event {
	return Event{EventID: id, SchemaVersion: 1, Time: "2026-09-08T01:00:00Z", System: "codex", SessionID: "writer", EventName: "PreToolUse", Decision: Decision{Kind: "block"}, Violations: []Violation{{Rule: "test", Mode: "block"}}}
}

func auditCounts(t *testing.T, database *sql.DB) (int, int) {
	t.Helper()
	fixture, err := os.ReadFile("testdata/count_batch.sql")
	if err != nil {
		t.Fatal(err)
	}
	var events, violations int
	if err := database.QueryRowContext(t.Context(), string(fixture)).Scan(&events, &violations); err != nil {
		t.Fatal(err)
	}
	return events, violations
}

func TestWriteEventsCommitsBatchOnceAndDeduplicatesChildren(t *testing.T) {
	database := testAuditDatabase(t)
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var commits atomic.Int32
	if err := connection.Raw(func(raw any) error {
		raw.(*sqlite3.SQLiteConn).RegisterCommitHook(func() int { commits.Add(1); return 0 })
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	events := []Event{testAuditEvent("first"), testAuditEvent("second"), testAuditEvent("first")}
	if err := WriteEvents(t.Context(), database, events); err != nil {
		t.Fatal(err)
	}
	if commits.Load() != 1 {
		t.Fatalf("commits = %d, want 1", commits.Load())
	}
	count, violations := auditCounts(t, database)
	if count != 2 || violations != 2 {
		t.Fatalf("rows = %d/%d", count, violations)
	}
}

func TestWriteEventsRollsBackWholeBatch(t *testing.T) {
	database := testAuditDatabase(t)
	fixture, err := os.ReadFile("testdata/fail_batch.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatal(err)
	}
	if err := WriteEvents(t.Context(), database, []Event{testAuditEvent("first"), testAuditEvent("second")}); err == nil {
		t.Fatal("expected batch failure")
	}
	count, violations := auditCounts(t, database)
	if count != 0 || violations != 0 {
		t.Fatalf("partial batch rows = %d/%d", count, violations)
	}
}

func TestWriteEventsHonorsCancellation(t *testing.T) {
	database := testAuditDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := WriteEvents(ctx, database, []Event{testAuditEvent("cancelled")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	count, _ := auditCounts(t, database)
	if count != 0 {
		t.Fatalf("cancelled rows = %d", count)
	}
}

func TestLoggerByteBoundaryAndCloseDrain(t *testing.T) {
	database := testAuditDatabase(t)
	event := testAuditEvent("boundary")
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	logger, err := NewEventLoggerWithOptions(t.Context(), nil, nil, LoggerOptions{SharedDB: database, BatchMaxBytes: len(encoded)})
	if err != nil {
		t.Fatal(err)
	}
	if err := logger.LogNormalizedDurable(t.Context(), NormalizedEntry{Event: event, Fingerprint: "boundary"}); err != nil {
		t.Fatal(err)
	}
	event.EventID += "x"
	if err := logger.LogNormalizedDurable(t.Context(), NormalizedEntry{Event: event, Fingerprint: "oversize"}); err == nil {
		t.Fatal("accepted oversized event")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	count, _ := auditCounts(t, database)
	if count != 1 {
		t.Fatalf("rows = %d", count)
	}
}

func TestLoggerQueueBytesReportsOverflowAndDrains(t *testing.T) {
	database := testAuditDatabase(t)
	var logs bytes.Buffer
	logger, err := NewEventLoggerWithOptions(t.Context(), nil, slog.New(slog.NewTextHandler(&logs, nil)), LoggerOptions{SharedDB: database, QueueLimit: 100, QueueMaxBytes: 350, BatchMaxItems: 2, BatchMaxBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("codex", "queue", "PreToolUse", "info", "first", nil)
	deadline := time.Now().Add(time.Second)
	for {
		logger.mu.Lock()
		empty := len(logger.queue) == 0
		logger.mu.Unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			_ = connection.Close()
			t.Fatal("worker did not consume first event")
		}
		time.Sleep(time.Millisecond)
	}
	logger.Log("codex", "queue", "PreToolUse", "info", "second", nil)
	logger.Log("codex", "queue", "PreToolUse", "info", "third", nil)
	logger.mu.Lock()
	queuedBytes := logger.queuedBytes
	queueSize := len(logger.queue)
	logger.mu.Unlock()
	if queuedBytes > 350 || queueSize != 1 {
		_ = connection.Close()
		t.Fatalf("queued bytes/items = %d/%d", queuedBytes, queueSize)
	}
	t.Logf("queued bytes=%d, byte limit=350, queued items=%d", queuedBytes, queueSize)
	if !strings.Contains(logs.String(), "dropping event") {
		_ = connection.Close()
		t.Fatal("overflow was not reported")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	count, _ := auditCounts(t, database)
	if count != 2 {
		t.Fatalf("drained rows = %d, want 2", count)
	}
}

func TestLoggerBoundsEachCommittedBatch(t *testing.T) {
	for _, options := range []LoggerOptions{{BatchMaxItems: 2, BatchMaxBytes: 2000}, {BatchMaxItems: 64, BatchMaxBytes: 350}} {
		t.Run(fmt.Sprintf("items-%d-bytes-%d", options.BatchMaxItems, options.BatchMaxBytes), func(t *testing.T) {
			database := testAuditDatabase(t)
			options.SharedDB = database
			logger, err := NewEventLoggerWithOptions(t.Context(), nil, nil, options)
			if err != nil {
				t.Fatal(err)
			}
			connection, err := database.Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			batches := make(chan int, 10)
			count := 0
			if err := connection.Raw(func(raw any) error {
				driver := raw.(*sqlite3.SQLiteConn)
				driver.RegisterUpdateHook(func(operation int, database, table string, rowID int64) {
					if operation == sqlite3.SQLITE_INSERT && table == "events" {
						count++
					}
				})
				driver.RegisterCommitHook(func() int { batches <- count; count = 0; return 0 })
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			logger.Log("codex", "batch", "PreToolUse", "info", "first", nil)
			waitForAuditConnectionWait(t, database)
			for index := range 5 {
				logger.Log("codex", "batch", "PreToolUse", "info", fmt.Sprintf("entry-%d", index), nil)
			}
			if err := connection.Close(); err != nil {
				t.Fatal(err)
			}
			if err := logger.Close(); err != nil {
				t.Fatal(err)
			}
			close(batches)
			total := 0
			sizes := make([]int, 0)
			for size := range batches {
				total += size
				sizes = append(sizes, size)
				limit := options.BatchMaxItems
				if options.BatchMaxBytes == 350 {
					limit = 1
				}
				if size > limit {
					t.Fatalf("batch size = %d, max = %d", size, limit)
				}
			}
			if total != 6 {
				t.Fatalf("committed events = %d", total)
			}
			if options.BatchMaxItems == 2 && len(sizes) != 4 {
				t.Fatalf("batches=%v, want [1 2 2 1]", sizes)
			}
			t.Logf("committed batch sizes=%v", sizes)
		})
	}
}

func waitForAuditConnectionWait(t *testing.T, database *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for database.Stats().WaitCount == 0 {
		if time.Now().After(deadline) {
			t.Fatal("writer did not wait for reserved connection")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestReviewConstructorCancellationStillDrains(t *testing.T) {
	database := testAuditDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logger, err := NewEventLoggerWithOptions(ctx, nil, nil, LoggerOptions{SharedDB: database})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("codex", "review", "PreToolUse", "info", "accepted-before-cancel", nil)
	cancel()
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	count, _ := auditCounts(t, database)
	if count != 1 {
		t.Fatalf("Close returned nil, persisted events=%d, want 1", count)
	}
}

func TestAuditQueryReturnsEventOwnedChildrenInOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	cfg := &config.Config{Audit: config.Audit{Outputs: config.AuditOutput{SQLite: config.AuditSQLiteOutput{Path: path}}}}
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
	if err != nil {
		t.Fatal(err)
	}
	bucket, err := catalog.EnsureCurrent(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := catalog.OpenWriter(t.Context(), bucket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	database := handle.Database
	target := testAuditEvent("target")
	target.SessionID = "children"
	target.Operation = Operation{CWD: "/repo", EffectiveCWD: "/repo/sub", Command: "run target", FilePath: "/repo/file"}
	target.Decision = Decision{Kind: "block", CanBlock: true, RulesChecked: []string{"first", "second"}, RulesMatched: []string{"second"}}
	target.Violations = []Violation{{Rule: "first", Mode: "audit", Message: "first match"}, {Rule: "second", Mode: "block", Message: "second match"}}
	empty := testAuditEvent("empty")
	empty.SessionID = "children"
	empty.Violations = nil
	unrelated := testAuditEvent("unrelated")
	if err := WriteEvents(t.Context(), database, []Event{unrelated, target, empty}); err != nil {
		t.Fatal(err)
	}

	records, _, err := QueryReadOnly(t.Context(), cfg, QueryFilter{SessionID: "children"})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records=%+v", records)
	}
	for _, record := range records {
		if record.EventID == "empty" {
			if len(record.Violations) != 0 {
				t.Fatalf("unrelated violations returned for empty event: %+v", record.Violations)
			}
			continue
		}
		if record.EventID != target.EventID || record.Operation != target.Operation || !reflect.DeepEqual(record.Decision, target.Decision) || !reflect.DeepEqual(record.Violations, target.Violations) {
			t.Fatalf("child records changed: %+v", record)
		}
	}
}

func TestLoggerOversizedAsyncEventUsesVisibleDrop(t *testing.T) {
	database := testAuditDatabase(t)
	var logs bytes.Buffer
	logger, err := NewEventLoggerWithOptions(t.Context(), nil, slog.New(slog.NewTextHandler(&logs, nil)), LoggerOptions{SharedDB: database, BatchMaxBytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	attrs := Attrs{"ti_command": NewStringValue(strings.Repeat("x", 1000))}
	logger.Log("codex", "oversize", "PreToolUse", "info", "event", attrs)
	if err := logger.LogDurable(t.Context(), "codex", "oversize", "PreToolUse", "info", "direct", attrs); err == nil {
		t.Fatal("oversized direct write succeeded")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "dropping event") {
		t.Fatal("oversized event was not reported")
	}
	count, _ := auditCounts(t, database)
	if count != 0 {
		t.Fatalf("oversized events were truncated or inserted: %d", count)
	}
}

func TestDurableWaitDoesNotBlockAsyncAdmission(t *testing.T) {
	database := testAuditDatabase(t)
	logger, err := NewEventLoggerWithOptions(t.Context(), nil, nil, LoggerOptions{SharedDB: database})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	written := make(chan error, 1)
	go func() {
		written <- logger.LogNormalizedDurable(ctx, NormalizedEntry{Event: testAuditEvent("waiting"), Fingerprint: "waiting"})
	}()
	waitForAuditConnectionWait(t, database)
	admitted := make(chan struct{})
	go func() { logger.Log("codex", "admission", "PreToolUse", "info", "event", nil); close(admitted) }()
	select {
	case <-admitted:
	case <-time.After(200 * time.Millisecond):
		t.Error("async admission waited for durable disk I/O")
	}
	cancel()
	if err := <-written; !errors.Is(err, context.Canceled) {
		t.Errorf("durable error=%v", err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	<-admitted
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCloseWaitsForDrain(t *testing.T) {
	database := testAuditDatabase(t)
	logger, err := NewEventLoggerWithOptions(t.Context(), nil, nil, LoggerOptions{SharedDB: database})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("codex", "close", "PreToolUse", "info", "event", nil)
	waitForAuditConnectionWait(t, database)
	first := make(chan error, 1)
	second := make(chan error, 1)
	go func() { first <- logger.Close() }()
	for {
		logger.mu.Lock()
		stopping := logger.stopping
		logger.mu.Unlock()
		if stopping {
			break
		}
		time.Sleep(time.Millisecond)
	}
	go func() { second <- logger.Close() }()
	early := false
	select {
	case err := <-second:
		early = true
		t.Errorf("second Close returned before drain: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if !early {
		if err := <-second; err != nil {
			t.Fatal(err)
		}
	}
	count, _ := auditCounts(t, database)
	if count != 1 {
		t.Fatalf("drained events=%d", count)
	}
}
