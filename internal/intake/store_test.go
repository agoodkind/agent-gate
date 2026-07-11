package intake_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/intake"
)

func TestSQLiteStoreAppendIsIdempotentByStableEventID(t *testing.T) {
	store := newTestStore(t)
	record := intake.Record{
		System:     "claude",
		SessionID:  "session-1",
		TurnID:     "turn-1",
		EventName:  "PreToolUse",
		ToolName:   "Bash",
		ToolUseID:  "toolu_1",
		RawPayload: []byte(`{"event":"pre","tool":"bash"}`),
		NormalizedJSON: []byte(`{
			"hook_event":"PreToolUse",
			"provider":"claude"
		}`),
	}

	first, err := store.Append(context.Background(), record)
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	second, err := store.Append(context.Background(), record)
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}

	if !first.Inserted {
		t.Fatal("first append inserted = false, want true")
	}
	if second.Inserted {
		t.Fatal("second append inserted = true, want false")
	}
	if first.EventID == "" {
		t.Fatal("first append event id = empty, want stable id")
	}
	if second.EventID != first.EventID {
		t.Fatalf("event id mismatch: first=%q second=%q", first.EventID, second.EventID)
	}

	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending records = %d, want 0", len(pending))
	}
}

func TestSQLiteStoreDeferredStateTransitions(t *testing.T) {
	store := newTestStore(t)
	appendResult, err := store.Append(context.Background(), intake.Record{
		EventID:    "evt_1",
		System:     "codex",
		SessionID:  "session-1",
		EventName:  "PostToolUse",
		RawPayload: []byte(`{"event":"post"}`),
		NormalizedJSON: []byte(`{
			"hook_event":"PostToolUse"
		}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := store.MarkDeferredPending(context.Background(), appendResult.EventID, appendResult.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending first: %v", err)
	}
	if err := store.MarkDeferredPending(context.Background(), appendResult.EventID, appendResult.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending second: %v", err)
	}

	pending, err := store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending before complete: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending records before complete = %d, want 1", len(pending))
	}
	if pending[0].DeferredState != intake.DeferredStatePending {
		t.Fatalf("pending state = %q, want %q", pending[0].DeferredState, intake.DeferredStatePending)
	}
	if pending[0].PendingAt == nil {
		t.Fatal("pending_at = nil, want timestamp")
	}
	if pending[0].CompletedAt != nil {
		t.Fatalf("completed_at = %v, want nil", pending[0].CompletedAt)
	}

	if err := store.MarkDeferredComplete(context.Background(), appendResult.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredComplete first: %v", err)
	}
	if err := store.MarkDeferredComplete(context.Background(), appendResult.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredComplete second: %v", err)
	}

	pending, err = store.ListDeferredPending(context.Background(), 0)
	if err != nil {
		t.Fatalf("ListDeferredPending after complete: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending records after complete = %d, want 0", len(pending))
	}
}

func TestSQLiteStoreReplayPendingUpdatesReplayMetadataInSequenceOrder(t *testing.T) {
	store := newTestStore(t)
	first := appendPendingRecord(t, store, "evt_1", time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC))
	second := appendPendingRecord(t, store, "evt_2", time.Date(2026, 5, 9, 12, 0, 1, 0, time.UTC))

	var replayed []intake.Record
	err := store.ReplayDeferredPending(context.Background(), 0, func(record intake.Record) error {
		replayed = append(replayed, record)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayDeferredPending: %v", err)
	}

	if len(replayed) != 2 {
		t.Fatalf("replayed records = %d, want 2", len(replayed))
	}
	if replayed[0].ReceiptID != first.ReceiptID {
		t.Fatalf("first replayed receipt = %d, want %d", replayed[0].ReceiptID, first.ReceiptID)
	}
	if replayed[1].ReceiptID != second.ReceiptID {
		t.Fatalf("second replayed receipt = %d, want %d", replayed[1].ReceiptID, second.ReceiptID)
	}
	for _, record := range replayed {
		if record.DeferredReplays != 1 {
			t.Fatalf("replay_count for %q = %d, want 1", record.EventID, record.DeferredReplays)
		}
		if record.LastReplayAt == nil {
			t.Fatalf("last_replay_at for %q = nil, want timestamp", record.EventID)
		}
		if record.DeferredState != intake.DeferredStatePending {
			t.Fatalf("state for %q = %q, want %q", record.EventID, record.DeferredState, intake.DeferredStatePending)
		}
	}
}

func TestSQLiteStoreReplayPendingSkipsReceiptCompletedAfterListing(t *testing.T) {
	store := newTestStore(t)
	first := appendPendingRecord(t, store, "evt_1", time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC))
	second := appendPendingRecord(t, store, "evt_2", time.Date(2026, 5, 9, 12, 0, 1, 0, time.UTC))

	var replayed []int64
	err := store.ReplayDeferredPending(context.Background(), 0, func(record intake.Record) error {
		replayed = append(replayed, record.ReceiptID)
		if record.ReceiptID == first.ReceiptID {
			return store.MarkDeferredComplete(context.Background(), second.ReceiptID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReplayDeferredPending: %v", err)
	}
	if len(replayed) != 1 || replayed[0] != first.ReceiptID {
		t.Fatalf("replayed receipts = %v, want [%d]", replayed, first.ReceiptID)
	}
}

func TestSQLiteStoreRejectsDeferredStateForUnknownEvent(t *testing.T) {
	store := newTestStore(t)
	err := store.MarkDeferredPending(context.Background(), "missing", 0)
	if !errors.Is(err, intake.ErrEventNotFound) {
		t.Fatalf("MarkDeferredPending error = %v, want ErrEventNotFound", err)
	}
}

func newTestStore(t *testing.T) *intake.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sqlite", "audit.db")
	store, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	return store
}

func appendPendingRecord(t *testing.T, store *intake.Store, eventID string, recordedAt time.Time) intake.AppendResult {
	t.Helper()
	appendResult, err := store.Append(context.Background(), intake.Record{
		EventID:    eventID,
		RecordedAt: recordedAt,
		System:     "claude",
		SessionID:  "session-1",
		EventName:  "PreToolUse",
		RawPayload: []byte(`{"event":"pre"}`),
		NormalizedJSON: []byte(`{
			"hook_event":"PreToolUse"
		}`),
	})
	if err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
	if err := store.MarkDeferredPending(context.Background(), appendResult.EventID, appendResult.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending %s: %v", eventID, err)
	}
	return appendResult
}
