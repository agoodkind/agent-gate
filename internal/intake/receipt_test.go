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

func TestOpenSQLiteMigratesLegacyDeferredRowsByNewestReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = database.Exec(legacyDeferredSchema)
	if err != nil {
		t.Fatalf("create legacy deferred database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite migration: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 1 || pending[0].ReceiptID != 2 || pending[0].EventID != "legacy-event" {
		t.Fatalf("migrated pending = %+v, want newest receipt 2", pending)
	}
	var claimAttempt int
	if err := store.Handle().QueryRow(`
		select claim_attempt from intake_deferred where receipt_id = 2
	`).Scan(&claimAttempt); err != nil {
		t.Fatalf("read migrated claim attempt: %v", err)
	}
	if claimAttempt != 1 {
		t.Fatalf("migrated claim attempt = %d, want replay count 1", claimAttempt)
	}
	var repairState string
	var repairError string
	err = store.Handle().QueryRow(`
		select state, repair_error
		from intake_deferred_repairs
		where event_id = 'unlinked-event'
	`).Scan(&repairState, &repairError)
	if err != nil {
		t.Fatalf("read deferred repair: %v", err)
	}
	if repairState != "pending" || repairError != "missing_receipt" {
		t.Fatalf("repair = (%q, %q)", repairState, repairError)
	}
}

const legacyDeferredSchema = `
create table intake_events (
    seq integer primary key autoincrement,
    event_id text not null unique,
    schema_version integer not null,
    recorded_at text not null,
    system text not null,
    session_id text not null,
    turn_id text not null,
    event_name text not null,
    tool_name text not null,
    tool_use_id text not null,
    cwd text not null,
    effective_cwd text not null,
    command text not null,
    file_path text not null,
    raw_payload blob not null,
    raw_payload_hash text not null,
    normalized_json text not null,
    env_fingerprint_json text not null default '{}'
);
create table intake_receipts (
    receipt_id integer primary key autoincrement,
    event_id text not null,
    received_at text not null
);
create table intake_deferred (
    event_id text primary key,
    state text not null,
    pending_at text,
    completed_at text,
    last_replay_at text,
    replay_count integer not null default 0
);
insert into intake_events values
    (1, 'legacy-event', 1, '2026-05-09T00:00:00Z', 'codex', 'session', '', 'PreToolUse', 'Shell', '', '/repo', '/repo', 'echo ok', '', x'7b7d', 'sha256:legacy', '{}', '{}'),
    (2, 'unlinked-event', 1, '2026-05-09T00:00:01Z', 'codex', 'session', '', 'PreToolUse', 'Shell', '', '/repo', '/repo', 'echo repair', '', x'7b7d', 'sha256:repair', '{}', '{}');
insert into intake_receipts values
    (1, 'legacy-event', '2026-05-09T00:00:00Z'),
    (2, 'legacy-event', '2026-05-09T00:00:01Z');
insert into intake_deferred values
    ('legacy-event', 'pending', '2026-05-09T00:00:00Z', null, null, 1),
    ('unlinked-event', 'pending', '2026-05-09T00:00:01Z', null, null, 0);
`

func TestOpenSQLiteMigratesPopulatedCurrentDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = database.Exec(`
		create table intake_events (
			seq integer primary key autoincrement,
			event_id text not null unique,
			schema_version integer not null,
			recorded_at text not null,
			system text not null,
			session_id text not null,
			turn_id text not null,
			event_name text not null,
			tool_name text not null,
			tool_use_id text not null,
			cwd text not null,
			effective_cwd text not null,
			command text not null,
			file_path text not null,
			raw_payload blob not null,
			raw_payload_hash text not null,
			normalized_json text not null,
			env_fingerprint_json text not null default '{}',
			hot_eval_latency_us integer
		);
		insert into intake_events values (
			1, 'legacy-event', 1, '2026-05-09T00:00:00Z', 'codex', 'session', '',
			'PreToolUse', 'Shell', '', '/repo', '/repo', 'echo ok', '', x'7b7d',
			'sha256:legacy', '{}', '{}', null
		);
		create table intake_receipts (
			receipt_id integer primary key autoincrement,
			event_id text not null,
			received_at text not null
		);
		insert into intake_receipts values (
			1, 'legacy-event', '2026-05-09T00:00:00Z'
		);
		create table intake_deferred (
			receipt_id integer primary key,
			event_id text not null,
			state text not null,
			pending_at text,
			completed_at text,
			last_replay_at text,
			replay_count integer not null default 0
		);
		insert into intake_deferred values (
			1, 'legacy-event', 'pending', '2026-05-09T00:00:00Z', null,
			'2026-05-09T00:00:01Z', 3
		);
	`)
	if err != nil {
		t.Fatalf("create populated current database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite migration: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}()

	assertTableCount(t, path, "intake_events", 1)
	assertTableCount(t, path, "intake_receipts", 1)
	var claimAttempt int
	if err := store.Handle().QueryRow(`
		select claim_attempt from intake_deferred where receipt_id = 1
	`).Scan(&claimAttempt); err != nil {
		t.Fatalf("read current-schema claim attempt: %v", err)
	}
	if claimAttempt != 3 {
		t.Fatalf("current-schema claim attempt = %d, want replay count 3", claimAttempt)
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
	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
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
