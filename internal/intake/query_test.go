package intake_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
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

func TestQueryRequiresMigrationForLegacyMixedDetail(t *testing.T) {
	path := installLegacyAuditFixture(t)
	result, err := intake.Query(context.Background(), queryConfig(path), intake.QueryFilter{
		EventID:           "event-legacy",
		IncludeNormalized: true,
		IncludeEnv:        true,
	})
	if err == nil {
		t.Fatalf("Query error = nil, returned legacy detail: %#v", result.Records)
	}
	if !strings.Contains(err.Error(), "requires migration") {
		t.Fatalf("Query error = %q, want migration-required error", err)
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
	pendingRecord, err := store.Get(context.Background(), pendingID)
	if err != nil {
		t.Fatalf("Get pending receipt: %v", err)
	}
	completeRecord, err := store.Get(context.Background(), completeID)
	if err != nil {
		t.Fatalf("Get complete receipt: %v", err)
	}
	if err := store.MarkDeferredPending(context.Background(), pendingID, pendingRecord.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	if err := store.MarkDeferredPending(context.Background(), completeID, completeRecord.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending complete row: %v", err)
	}
	if err := store.MarkDeferredComplete(context.Background(), completeRecord.ReceiptID); err != nil {
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
	if withJSON.Records[0].Detail.State != auditstorage.DetailStateAvailable {
		t.Fatalf("detail state = %q, want available", withJSON.Records[0].Detail.State)
	}
}

func TestQueryReportsExpiredDetailAndOmitsMissingContent(t *testing.T) {
	store, path := newQueryTestStore(t)
	result, err := store.Append(t.Context(), intake.Record{
		EventID:            "evt-expired",
		RecordedAt:         time.Date(2026, 5, 9, 21, 0, 0, 0, time.UTC),
		System:             "codex",
		SessionID:          "session-expired",
		EventName:          "PreToolUse",
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
		EnvFingerprint:     map[string]string{"AI_AGENT": "codex"},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Handle().Exec(`
		delete from intake_event_details
		where event_id = ? and detail_class in (?, ?, ?)
	`, result.EventID, auditstorage.DetailClassNormalizedInput,
		auditstorage.DetailClassProviderEvidence,
		auditstorage.DetailClassEnvironmentEvidence); err != nil {
		t.Fatalf("delete expired detail: %v", err)
	}
	if _, err := store.Handle().Exec(`
		update intake_event_detail_manifest
		set available_classes_json = '["wire_input"]', state = 'expired',
			state_changed_at = '2026-05-10T00:00:00Z'
		where event_id = ?
	`, result.EventID); err != nil {
		t.Fatalf("mark detail expired: %v", err)
	}

	queryResult, err := intake.Query(t.Context(), queryConfig(path), intake.QueryFilter{
		EventID: result.EventID, IncludeNormalized: true, IncludeEnv: true,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	encoded, err := json.Marshal(queryResult.Records[0])
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(encoded)
	if !strings.Contains(text, `"detail":{"state":"expired"`) {
		t.Fatalf("detail state missing: %s", text)
	}
	for _, missing := range []string{"classification", "normalized_json", "env_fingerprint"} {
		if strings.Contains(text, `"`+missing+`"`) {
			t.Fatalf("expired field %q present: %s", missing, text)
		}
	}
}

func TestQueryHonorsTerminalStateWhenDetailRowsRemain(t *testing.T) {
	for _, terminalState := range []auditstorage.DetailState{
		auditstorage.DetailStateExpired,
		auditstorage.DetailStateNotRecorded,
	} {
		t.Run(string(terminalState), func(t *testing.T) {
			store, path := newQueryTestStore(t)
			eventID := appendDetailStateRecord(t, store, "evt-terminal-"+string(terminalState))
			if _, err := store.Handle().Exec(`
				update intake_event_detail_manifest set state = ? where event_id = ?
			`, terminalState, eventID); err != nil {
				t.Fatalf("mark detail %s: %v", terminalState, err)
			}

			result, err := intake.Query(t.Context(), queryConfig(path), intake.QueryFilter{
				EventID: eventID, IncludeNormalized: true, IncludeEnv: true,
			})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			record := result.Records[0]
			if record.Detail.State != terminalState {
				t.Fatalf("detail state = %q, want %q", record.Detail.State, terminalState)
			}
			encoded, err := json.Marshal(record)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, field := range []string{
				"classification", "normalized_json", "env_fingerprint",
			} {
				if strings.Contains(string(encoded), `"`+field+`"`) {
					t.Fatalf("terminal field %q present: %s", field, encoded)
				}
			}
		})
	}
}

func TestQueryReportsProtectedAndNotRecordedDetail(t *testing.T) {
	store, path := newQueryTestStore(t)
	protected := appendDetailStateRecord(t, store, "evt-protected")
	protectedReceipt, err := store.Get(t.Context(), protected)
	if err != nil {
		t.Fatalf("Get protected receipt: %v", err)
	}
	if err := store.MarkDeferredPending(
		t.Context(), protected, protectedReceipt.ReceiptID,
	); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	if _, err := store.Handle().Exec(`
		update intake_event_detail_manifest set state = 'protected' where event_id = ?
	`, protected); err != nil {
		t.Fatalf("mark detail protected: %v", err)
	}

	notRecorded := appendDetailStateRecord(t, store, "evt-not-recorded")
	if _, err := store.Handle().Exec(`
		delete from intake_event_details
		where event_id = ? and detail_class in (?, ?, ?)
	`, notRecorded, auditstorage.DetailClassNormalizedInput,
		auditstorage.DetailClassProviderEvidence,
		auditstorage.DetailClassEnvironmentEvidence); err != nil {
		t.Fatalf("delete not-recorded detail: %v", err)
	}
	if _, err := store.Handle().Exec(`
		update intake_event_detail_manifest
		set recorded_classes_json = '["wire_input"]',
			available_classes_json = '["wire_input"]', state = 'not_recorded'
		where event_id = ?
	`, notRecorded); err != nil {
		t.Fatalf("mark detail not recorded: %v", err)
	}

	for _, testCase := range []struct {
		eventID string
		state   auditstorage.DetailState
		present bool
	}{
		{eventID: protected, state: auditstorage.DetailStateProtected, present: true},
		{eventID: notRecorded, state: auditstorage.DetailStateNotRecorded, present: false},
	} {
		result, err := intake.Query(t.Context(), queryConfig(path), intake.QueryFilter{
			EventID: testCase.eventID, IncludeNormalized: true, IncludeEnv: true,
		})
		if err != nil {
			t.Fatalf("Query %s: %v", testCase.eventID, err)
		}
		record := result.Records[0]
		if record.Detail.State != testCase.state {
			t.Fatalf("%s detail state = %q, want %q", testCase.eventID, record.Detail.State, testCase.state)
		}
		contentPresent := len(record.Classification) > 0 && len(record.NormalizedJSON) > 0 &&
			len(record.EnvFingerprint) > 0
		if contentPresent != testCase.present {
			t.Fatalf("%s content present = %t, want %t", testCase.eventID, contentPresent, testCase.present)
		}
	}
}

func TestQueryReportsAvailableWhenOnlyUnrequestedClassIsProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sqlite", "audit.db")
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configBody := `[audit.storage]
profile = "balanced"

[audit.storage.detail]
wire_input = false
normalized_input = false

[audit.outputs.sqlite]
path = "` + path + `"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	store, err := intake.OpenSQLiteWithOptions(t.Context(), intake.SQLiteOptions{
		Path: path, Policy: cfg.AuditStoragePolicy(), Log: nil,
	})
	if err != nil {
		t.Fatalf("OpenSQLiteWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	result, err := store.Append(t.Context(), intake.Record{
		EventID: "evt-wire-protected", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session-wire-protected", EventName: "PreToolUse",
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	queryResult, err := intake.Query(t.Context(), cfg, intake.QueryFilter{
		EventID: result.EventID,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	record := queryResult.Records[0]
	if record.Detail.State != auditstorage.DetailStateAvailable {
		t.Fatalf("detail state = %q, want available", record.Detail.State)
	}
	if string(record.Classification) != `{"provider":"codex"}` {
		t.Fatalf("classification = %s, want recorded provider evidence", record.Classification)
	}
	protectedResult, err := intake.Query(t.Context(), cfg, intake.QueryFilter{
		EventID: result.EventID, IncludeNormalized: true,
	})
	if err != nil {
		t.Fatalf("Query protected class: %v", err)
	}
	if protectedResult.Records[0].Detail.State != auditstorage.DetailStateProtected {
		t.Fatalf(
			"requested protected detail state = %q, want protected",
			protectedResult.Records[0].Detail.State,
		)
	}
}

func TestQueryReportsProtectedWhenLivePolicyDisablesAvailableClass(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sqlite", "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	result, err := store.Append(t.Context(), intake.Record{
		EventID: "evt-policy-cutover", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session-policy-cutover", EventName: "PreToolUse",
		RawPayload:         []byte(`{"wire":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "config.toml")
	configBody := `[audit.storage]
profile = "minimal"

[audit.outputs.sqlite]
path = "` + path + `"
`
	if err := os.WriteFile(configPath, []byte(configBody), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
	cfg, err := config.LoadExisting(configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}

	queryResult, err := intake.Query(t.Context(), cfg, intake.QueryFilter{
		EventID: result.EventID,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	record := queryResult.Records[0]
	if record.Detail.State != auditstorage.DetailStateProtected {
		t.Fatalf("detail state = %q, want protected", record.Detail.State)
	}
	if string(record.Classification) != `{"provider":"codex"}` {
		t.Fatalf("classification = %s, want protected provider evidence", record.Classification)
	}
}

func appendDetailStateRecord(t *testing.T, store *intake.Store, eventID string) string {
	t.Helper()
	result, err := store.Append(t.Context(), intake.Record{
		EventID: eventID, RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session-detail", EventName: "PreToolUse",
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
		EnvFingerprint:     map[string]string{"AI_AGENT": "codex"},
	})
	if err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
	return result.EventID
}

func TestQueryIncludesClassification(t *testing.T) {
	store, path := newQueryTestStore(t)
	classification := json.RawMessage(`{
		"input":{"provider_hint":"unknown"},
		"resolved_provider":"cursor",
		"confidence":"high",
		"evidence":[{"source":"payload","result":"match"}],
		"result":"resolved"
	}`)
	appendResult, err := store.Append(context.Background(), intake.Record{
		EventID:            "evt_classification",
		System:             "cursor",
		SessionID:          "session-classification",
		EventName:          "preToolUse",
		RawPayload:         []byte(`{"conversation_id":"session-classification"}`),
		NormalizedJSON:     []byte(`{"conversation_id":"session-classification"}`),
		ClassificationJSON: classification,
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	result, err := intake.Query(
		context.Background(),
		queryConfig(path),
		intake.QueryFilter{EventID: appendResult.EventID},
	)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(result.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(result.Records))
	}
	if !json.Valid(result.Records[0].Classification) {
		t.Fatalf("classification is invalid JSON: %s", result.Records[0].Classification)
	}
	if string(result.Records[0].Classification) != string(mustCompactJSON(t, classification)) {
		t.Fatalf(
			"classification = %s, want %s",
			result.Records[0].Classification,
			mustCompactJSON(t, classification),
		)
	}
}

func mustCompactJSON(t *testing.T, value json.RawMessage) json.RawMessage {
	t.Helper()
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, value); err != nil {
		t.Fatalf("compact JSON: %v", err)
	}
	return json.RawMessage(compacted.String())
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
