package intake_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func TestQueryHandlesMissingAndEmptyIntakeHistory(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.db")
	missingResult, err := intake.Query(context.Background(), queryConfig(missingPath), intake.QueryFilter{})
	if err != nil {
		t.Fatalf("Query missing sqlite: %v", err)
	}
	if len(missingResult.Records) != 0 {
		t.Fatalf("missing sqlite records = %d, want 0", len(missingResult.Records))
	}
	if !strings.Contains(missingResult.Note, "no durable seen-event history") {
		t.Fatalf("missing sqlite note = %q, want friendly empty note", missingResult.Note)
	}

	_, emptyPath := newQueryTestStore(t)
	emptyResult, err := intake.Query(context.Background(), queryConfig(emptyPath), intake.QueryFilter{})
	if err != nil {
		t.Fatalf("Query empty sqlite: %v", err)
	}
	if len(emptyResult.Records) != 0 {
		t.Fatalf("empty sqlite records = %d, want 0", len(emptyResult.Records))
	}
	if !strings.Contains(emptyResult.Note, "no seen events") {
		t.Fatalf("empty sqlite note = %q, want friendly empty note", emptyResult.Note)
	}
}

func TestQueryHandlesMissingIntakeTablesAsEmptyHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	_, err = db.Exec(`create table unrelated (id integer primary key)`)
	if err != nil {
		t.Fatalf("create unrelated schema: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	result, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{})
	if err != nil {
		t.Fatalf("Query missing intake tables: %v", err)
	}
	if len(result.Records) != 0 {
		t.Fatalf("missing table records = %d, want 0", len(result.Records))
	}
	if !strings.Contains(result.Note, "no durable seen-event history") {
		t.Fatalf("missing table note = %q, want friendly empty note", result.Note)
	}
}

func TestQueryClampsRangesToFirstIntakeRecord(t *testing.T) {
	store, path := newQueryTestStore(t)
	firstRecordedAt := time.Date(2026, 5, 9, 19, 30, 0, 0, time.UTC)
	appendQueryRecord(t, store, "evt_first", firstRecordedAt, "claude", "session-1", "PreToolUse", "Bash")
	appendQueryRecord(t, store, "evt_second", firstRecordedAt.Add(time.Minute), "codex", "session-2", "PostToolUse", "Shell")

	preRange, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{
		Since: firstRecordedAt.Add(-2 * time.Hour),
		Until: firstRecordedAt.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("Query pre-range: %v", err)
	}
	if len(preRange.Records) != 0 {
		t.Fatalf("pre-range records = %d, want 0", len(preRange.Records))
	}
	if !strings.Contains(preRange.Note, firstRecordedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("pre-range note = %q, want dynamic first-record timestamp", preRange.Note)
	}

	spanningRange, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{
		Since: firstRecordedAt.Add(-2 * time.Hour),
		Until: firstRecordedAt.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Query spanning range: %v", err)
	}
	if len(spanningRange.Records) != 2 {
		t.Fatalf("spanning range records = %d, want 2", len(spanningRange.Records))
	}
	if !strings.Contains(spanningRange.Note, firstRecordedAt.Format(time.RFC3339Nano)) {
		t.Fatalf("spanning range note = %q, want dynamic first-record timestamp", spanningRange.Note)
	}
	if !strings.Contains(spanningRange.Note, "clamped") {
		t.Fatalf("spanning range note = %q, want clamp note", spanningRange.Note)
	}
}

func TestQueryFiltersSeenEventsAndRendersDeferredStates(t *testing.T) {
	store, path := newQueryTestStore(t)
	baseTime := time.Date(2026, 5, 9, 20, 0, 0, 0, time.UTC)
	noneID := appendQueryRecord(t, store, "evt_none", baseTime, "claude", "session-1", "PreToolUse", "Bash")
	pendingID := appendQueryRecord(t, store, "evt_pending", baseTime.Add(time.Minute), "codex", "session-2", "PostToolUse", "Shell")
	completeID := appendQueryRecord(t, store, "evt_complete", baseTime.Add(2*time.Minute), "gemini", "session-3", "BeforeTool", "WriteFile")
	if err := store.MarkDeferredPending(context.Background(), pendingID); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	if err := store.MarkDeferredPending(context.Background(), completeID); err != nil {
		t.Fatalf("MarkDeferredPending complete row: %v", err)
	}
	if err := store.MarkDeferredComplete(context.Background(), completeID); err != nil {
		t.Fatalf("MarkDeferredComplete: %v", err)
	}

	assertQueryEventIDs(t, path, intake.QueryFilter{System: "claude"}, noneID)
	assertQueryEventIDs(t, path, intake.QueryFilter{SessionID: "session-2"}, pendingID)
	assertQueryEventIDs(t, path, intake.QueryFilter{EventName: "BeforeTool"}, completeID)
	assertQueryEventIDs(t, path, intake.QueryFilter{ToolName: "Shell"}, pendingID)
	assertQueryEventIDs(t, path, intake.QueryFilter{EventID: noneID}, noneID)
	assertQueryEventIDs(t, path, intake.QueryFilter{DeferredState: string(intake.DeferredStateNone)}, noneID)
	assertQueryEventIDs(t, path, intake.QueryFilter{DeferredState: string(intake.DeferredStatePending)}, pendingID)
	assertQueryEventIDs(t, path, intake.QueryFilter{DeferredState: string(intake.DeferredStateComplete)}, completeID)
	assertQueryEventIDs(t, path, intake.QueryFilter{
		Since: baseTime.Add(30 * time.Second),
		Until: baseTime.Add(90 * time.Second),
	}, pendingID)
	assertQueryEventIDs(t, path, intake.QueryFilter{Limit: 1}, completeID)

	allRecords, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{})
	if err != nil {
		t.Fatalf("Query all records: %v", err)
	}
	statesByEventID := make(map[string]intake.DeferredState)
	for _, record := range allRecords.Records {
		statesByEventID[record.EventID] = record.Deferred.State
	}
	if statesByEventID[noneID] != intake.DeferredStateNone {
		t.Fatalf("state for %s = %q, want none", noneID, statesByEventID[noneID])
	}
	if statesByEventID[pendingID] != intake.DeferredStatePending {
		t.Fatalf("state for %s = %q, want pending", pendingID, statesByEventID[pendingID])
	}
	if statesByEventID[completeID] != intake.DeferredStateComplete {
		t.Fatalf("state for %s = %q, want complete", completeID, statesByEventID[completeID])
	}
}

func TestQueryIncludesNormalizedAndEnvJSONOnlyWhenRequested(t *testing.T) {
	store, path := newQueryTestStore(t)
	appendResult, err := store.Append(context.Background(), intake.Record{
		EventID:    "evt_json",
		RecordedAt: time.Date(2026, 5, 9, 21, 0, 0, 0, time.UTC),
		System:     "claude",
		SessionID:  "session-json",
		EventName:  "PreToolUse",
		ToolName:   "Bash",
		RawPayload: []byte(`{"secret":"raw"}`),
		NormalizedJSON: []byte(`{
			"hook_event_name": "PreToolUse",
			"tool_name": "Bash"
		}`),
		EnvFingerprint: map[string]string{
			"AI_AGENT": "claude",
		},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	withoutJSON, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{EventID: appendResult.EventID})
	if err != nil {
		t.Fatalf("Query without JSON: %v", err)
	}
	encodedWithout, err := json.Marshal(withoutJSON.Records[0])
	if err != nil {
		t.Fatalf("Marshal without JSON: %v", err)
	}
	if strings.Contains(string(encodedWithout), "normalized_json") || strings.Contains(string(encodedWithout), "env_fingerprint") {
		t.Fatalf("unexpected JSON fields without flags: %s", string(encodedWithout))
	}
	if strings.Contains(string(encodedWithout), "secret") {
		t.Fatalf("raw payload leaked in query JSON: %s", string(encodedWithout))
	}

	withJSON, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{
		EventID:           appendResult.EventID,
		IncludeNormalized: true,
		IncludeEnv:        true,
	})
	if err != nil {
		t.Fatalf("Query with JSON: %v", err)
	}
	encodedWith, err := json.Marshal(withJSON.Records[0])
	if err != nil {
		t.Fatalf("Marshal with JSON: %v", err)
	}
	if !strings.Contains(string(encodedWith), "normalized_json") {
		t.Fatalf("normalized_json missing with flag: %s", string(encodedWith))
	}
	if !strings.Contains(string(encodedWith), "env_fingerprint") {
		t.Fatalf("env_fingerprint missing with flag: %s", string(encodedWith))
	}
	if strings.Contains(string(encodedWith), "secret") {
		t.Fatalf("raw payload leaked in query JSON: %s", string(encodedWith))
	}
}

func queryConfig(path string) *config.Config {
	return &config.Config{
		Audit: config.Audit{
			Outputs: config.AuditOutput{
				SQLite: config.AuditSQLiteOutput{
					Path: path,
				},
			},
		},
	}
}

func newQueryTestStore(t *testing.T) (*intake.Store, string) {
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
	return store, path
}

func appendQueryRecord(t *testing.T, store *intake.Store, eventID string, recordedAt time.Time, system string, sessionID string, eventName string, toolName string) string {
	t.Helper()
	appendResult, err := store.Append(context.Background(), intake.Record{
		EventID:    eventID,
		RecordedAt: recordedAt,
		System:     system,
		SessionID:  sessionID,
		EventName:  eventName,
		ToolName:   toolName,
		RawPayload: []byte(`{"event":"test"}`),
		NormalizedJSON: []byte(`{
			"hook_event_name": "test"
		}`),
	})
	if err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
	return appendResult.EventID
}

func assertQueryEventIDs(t *testing.T, path string, filter intake.QueryFilter, wantEventIDs ...string) {
	t.Helper()
	result, err := intake.Query(context.Background(), queryConfig(path), filter)
	if err != nil {
		t.Fatalf("Query %#v: %v", filter, err)
	}
	if len(result.Records) != len(wantEventIDs) {
		t.Fatalf("records = %d, want %d for filter %#v", len(result.Records), len(wantEventIDs), filter)
	}
	for i, wantEventID := range wantEventIDs {
		if result.Records[i].EventID != wantEventID {
			t.Fatalf("record[%d].EventID = %q, want %q for filter %#v", i, result.Records[i].EventID, wantEventID, filter)
		}
	}
}
