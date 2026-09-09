package intake_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/intake"
)

func TestAppendRecordsEveryReceiptForCanonicalEvent(t *testing.T) {
	store, path := newReceiptTestStore(t)
	record := receiptTestRecord()

	first, err := store.Append(context.Background(), record)
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	second, err := store.Append(context.Background(), record)
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}

	if first.ReceiptID <= 0 || second.ReceiptID <= first.ReceiptID {
		t.Fatalf("receipt ids = %d, %d, want positive increasing ids", first.ReceiptID, second.ReceiptID)
	}
	if first.EventID != second.EventID || !first.Inserted || second.Inserted {
		t.Fatalf("append results = %#v, %#v", first, second)
	}
	assertTableCount(t, path, "intake_events", 1)
	assertTableCount(t, path, "intake_receipts", 2)
}

func TestDuplicateEventReceiptsRemainIndependentlyPending(t *testing.T) {
	store, _ := newReceiptTestStore(t)
	first, err := store.Append(context.Background(), receiptTestRecord())
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	second, err := store.Append(context.Background(), receiptTestRecord())
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	loaded, err := store.GetReceipt(context.Background(), first.ReceiptID)
	if err != nil {
		t.Fatalf("GetReceipt before deferred state: %v", err)
	}
	if loaded.DeferredState != intake.DeferredStateNone || loaded.DeferredReplays != 0 {
		t.Fatalf("new receipt deferred metadata = %+v", loaded)
	}
	for _, receiptID := range []int64{first.ReceiptID, second.ReceiptID} {
		if err := store.MarkDeferredPending(context.Background(), first.EventID, receiptID); err != nil {
			t.Fatalf("MarkDeferredPending %d: %v", receiptID, err)
		}
	}
	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 2 || pending[0].ReceiptID != first.ReceiptID || pending[1].ReceiptID != second.ReceiptID {
		t.Fatalf("pending receipt order = %+v", pending)
	}
	loaded, err = store.GetReceipt(context.Background(), first.ReceiptID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if loaded.EventID != first.EventID || loaded.ReceivedAt.IsZero() {
		t.Fatalf("receipt record = %+v", loaded)
	}
	var replayed []int64
	err = store.ReplayDeferredPending(context.Background(), 0, func(record intake.Record) error {
		replayed = append(replayed, record.ReceiptID)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayDeferredPending: %v", err)
	}
	if len(replayed) != 2 || replayed[0] != first.ReceiptID || replayed[1] != second.ReceiptID {
		t.Fatalf("replayed receipts = %v", replayed)
	}
	if err := store.MarkDeferredComplete(context.Background(), first.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredComplete: %v", err)
	}
	pending, err = store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending after complete: %v", err)
	}
	if len(pending) != 1 || pending[0].ReceiptID != second.ReceiptID {
		t.Fatalf("remaining pending receipts = %+v", pending)
	}
}

func TestMarkDeferredPendingRejectsMismatchedReceiptEvent(t *testing.T) {
	store, _ := newReceiptTestStore(t)
	first, err := store.Append(context.Background(), receiptTestRecord())
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	other := receiptTestRecord()
	other.EventID = "other-event"
	second, err := store.Append(context.Background(), other)
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	err = store.MarkDeferredPending(context.Background(), first.EventID, second.ReceiptID)
	if !errors.Is(err, intake.ErrReceiptEventMismatch) {
		t.Fatalf("MarkDeferredPending error = %v, want ErrReceiptEventMismatch", err)
	}
}

func TestAppendRollsBackEventWhenReceiptInsertFails(t *testing.T) {
	store, path := newReceiptTestStore(t)
	_, err := store.Handle().Exec(`
		create trigger fail_intake_receipt
		before insert on intake_receipts
		begin
			select raise(abort, 'receipt failure');
		end
	`)
	if err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	_, err = store.Append(context.Background(), receiptTestRecord())
	if err == nil {
		t.Fatal("Append error = nil, want receipt failure")
	}
	assertTableCount(t, path, "intake_events", 0)
	assertTableCount(t, path, "intake_receipts", 0)
}

func TestConcurrentAppendsKeepReceiptsOnCanonicalEvent(t *testing.T) {
	store, path := newReceiptTestStore(t)
	const appendCount = 16
	results := make(chan intake.AppendResult, appendCount)
	errors := make(chan error, appendCount)
	var waitGroup sync.WaitGroup
	for range appendCount {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			result, err := store.Append(context.Background(), receiptTestRecord())
			if err != nil {
				errors <- err
				return
			}
			results <- result
		}()
	}
	waitGroup.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}

	receiptIDs := make(map[int64]bool, appendCount)
	eventID := ""
	insertedCount := 0
	for result := range results {
		receiptIDs[result.ReceiptID] = true
		if eventID == "" {
			eventID = result.EventID
		}
		if result.EventID != eventID {
			t.Fatalf("event id = %q, want %q", result.EventID, eventID)
		}
		if result.Inserted {
			insertedCount++
		}
	}
	if len(receiptIDs) != appendCount || insertedCount != 1 {
		t.Fatalf("receipt ids = %d, inserted = %d", len(receiptIDs), insertedCount)
	}
	assertTableCount(t, path, "intake_events", 1)
	assertTableCount(t, path, "intake_receipts", appendCount)
}

func newReceiptTestStore(t *testing.T) (*intake.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := openFixtureIntake(t, context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Handle().Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return store, path
}

func receiptTestRecord() intake.Record {
	return intake.Record{
		System:         "codex",
		SessionID:      "session-1",
		EventName:      "PreToolUse",
		ToolName:       "Shell",
		RawPayload:     []byte(`{"event":"pre"}`),
		NormalizedJSON: []byte(`{"event":"pre"}`),
	}
}

func assertTableCount(t *testing.T, path string, table string, want int) {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	}()
	var count int
	if err := database.QueryRow("select count(*) from " + table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}
