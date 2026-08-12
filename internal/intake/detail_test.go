package intake_test

import (
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

var intakeDetailClasses = []string{
	"environment_evidence",
	"normalized_input",
	"provider_evidence",
	"wire_input",
}

func TestAppendCommitsSummaryAndConfiguredDetailTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, fullDetailPolicy())
	record := populatedDetailRecord("event-detail")

	first, err := store.Append(t.Context(), record)
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	second, err := store.Append(t.Context(), record)
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}
	if !first.Inserted || second.Inserted || second.ReceiptID <= first.ReceiptID {
		t.Fatalf("append results = %#v, %#v", first, second)
	}
	assertDetailRecord(t, store, second.ReceiptID, record)
	assertDetailRows(t, store.Handle(), record.EventID, auditstorage.DetailStateAvailable)
	assertDetailTableCount(t, store.Handle(), "intake_events", 1)
	assertDetailTableCount(t, store.Handle(), "intake_receipts", 2)
}

func TestOpenSQLiteMigratesLegacyIntakeDetail(t *testing.T) {
	t.Run("fidelity and idempotence", func(t *testing.T) {
		path := installLegacyAuditFixture(t)
		store := openDetailStore(t, path, fullDetailPolicy())

		legacy, err := store.GetReceipt(t.Context(), 1)
		if err != nil {
			t.Fatalf("GetReceipt migrated: %v", err)
		}
		assertLegacyDetailValues(t, legacy)
		assertDetailRows(t, store.Handle(), legacy.EventID, auditstorage.DetailStateAvailable)
		assertSchemaVersion(t, store.Handle(), 2)
		appliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), store.Handle(), 2)
		if err != nil {
			t.Fatalf("MigrationAppliedAt first open: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close first store: %v", err)
		}

		reopened := openDetailStore(t, path, fullDetailPolicy())
		reopenedRecord, err := reopened.GetReceipt(t.Context(), 1)
		if err != nil {
			t.Fatalf("GetReceipt reopened: %v", err)
		}
		assertLegacyDetailValues(t, reopenedRecord)
		reopenedAppliedAt, err := auditstorage.MigrationAppliedAt(t.Context(), reopened.Handle(), 2)
		if err != nil {
			t.Fatalf("MigrationAppliedAt reopen: %v", err)
		}
		if !reopenedAppliedAt.Equal(appliedAt) {
			t.Fatalf("migration timestamp changed from %s to %s", appliedAt, reopenedAppliedAt)
		}
	})

	t.Run("failure rolls back copied detail", func(t *testing.T) {
		path := installLegacyAuditFixture(t)
		database := openSQLiteHandle(t, path)
		if _, err := database.ExecContext(t.Context(), `
			create table intake_event_detail_manifest (id integer primary key)
		`); err != nil {
			t.Fatalf("install migration failure: %v", err)
		}
		if err := database.Close(); err != nil {
			t.Fatalf("close fixture handle: %v", err)
		}

		store, err := intake.OpenSQLiteWithOptions(t.Context(), intake.SQLiteOptions{
			Path: path, Policy: fullDetailPolicy(), Log: nil,
		})
		if err == nil {
			_ = store.Close()
			t.Fatal("OpenSQLiteWithOptions error = nil, want migration failure")
		}
		database = openSQLiteHandle(t, path)
		defer func() { _ = database.Close() }()
		var rawPayload []byte
		if err := database.QueryRowContext(
			t.Context(), `select raw_payload from intake_events where event_id = 'event-legacy'`,
		).Scan(&rawPayload); err != nil {
			t.Fatalf("read legacy detail after failure: %v", err)
		}
		if string(rawPayload) != `{"wire":"legacy"}` {
			t.Fatalf("legacy raw payload after failure = %q, want preserved", rawPayload)
		}
		version, err := auditstorage.SchemaVersion(t.Context(), database)
		if err != nil {
			t.Fatalf("SchemaVersion after failure: %v", err)
		}
		if version != 1 {
			t.Fatalf("schema version after failure = %d, want 1", version)
		}
		var detailTable string
		err = database.QueryRowContext(
			t.Context(),
			`select name from sqlite_schema where type = 'table' and name = 'intake_event_details'`,
		).Scan(&detailTable)
		if !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("detail table lookup error = %v, want sql.ErrNoRows", err)
		}
	})
}

func assertLegacyDetailValues(t *testing.T, record intake.Record) {
	t.Helper()
	if got, want := string(record.RawPayload), `{"wire":"legacy"}`; got != want {
		t.Fatalf("raw payload = %q, want %q", got, want)
	}
	if got, want := string(record.NormalizedJSON), `{"normalized":"legacy"}`; got != want {
		t.Fatalf("normalized JSON = %q, want %q", got, want)
	}
	if got, want := string(record.ClassificationJSON), `{"resolved_provider":"codex","result":"resolved"}`; got != want {
		t.Fatalf("classification JSON = %q, want %q", got, want)
	}
	wantEnvironment := map[string]string{"CODEX_THREAD_ID": "legacy-thread"}
	if !reflect.DeepEqual(record.EnvFingerprint, wantEnvironment) {
		t.Fatalf("environment = %#v, want %#v", record.EnvFingerprint, wantEnvironment)
	}
}

func TestPendingReplayProtectsDisabledIntakeDetail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, minimalDetailPolicy())
	record := populatedDetailRecord("event-protected")
	result, err := store.Append(t.Context(), record)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.MarkDeferredPending(t.Context(), result.EventID, result.ReceiptID); err != nil {
		t.Fatalf("MarkDeferredPending: %v", err)
	}
	assertDetailRows(t, store.Handle(), result.EventID, auditstorage.DetailStateProtected)
	if err := store.Close(); err != nil {
		t.Fatalf("Close before replay: %v", err)
	}

	reopened := openDetailStore(t, path, minimalDetailPolicy())
	var replayed intake.Record
	if err := reopened.ReplayDeferredPending(t.Context(), 1, func(record intake.Record) error {
		replayed = record
		return nil
	}); err != nil {
		t.Fatalf("ReplayDeferredPending: %v", err)
	}
	if replayed.ReceiptID != result.ReceiptID {
		t.Fatalf("replayed receipt = %d, want %d", replayed.ReceiptID, result.ReceiptID)
	}
	assertRecordDetailEqual(t, replayed, record)
}

func TestAppendRollsBackSummaryWhenDetailWriteFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store := openDetailStore(t, path, fullDetailPolicy())
	if _, err := store.Handle().ExecContext(t.Context(), `
		create trigger fail_intake_detail before insert on intake_event_details
		begin select raise(abort, 'forced detail failure'); end
	`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if _, err := store.Append(t.Context(), populatedDetailRecord("event-rollback")); err == nil {
		t.Fatal("Append error = nil, want detail failure")
	}
	assertDetailTableCount(t, store.Handle(), "intake_events", 0)
	assertDetailTableCount(t, store.Handle(), "intake_event_details", 0)
	assertDetailTableCount(t, store.Handle(), "intake_event_detail_manifest", 0)
	assertDetailTableCount(t, store.Handle(), "intake_receipts", 0)
}

func openDetailStore(
	t *testing.T,
	path string,
	policy config.AuditStoragePolicy,
) *intake.Store {
	t.Helper()
	store, err := intake.OpenSQLiteWithOptions(t.Context(), intake.SQLiteOptions{
		Path: path, Policy: policy, Log: nil,
	})
	if err != nil {
		t.Fatalf("OpenSQLiteWithOptions: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func populatedDetailRecord(eventID string) intake.Record {
	return intake.Record{
		EventID: eventID, System: "codex", SessionID: "session-detail",
		TurnID: "turn-detail", EventName: "PreToolUse", ToolName: "Shell",
		ToolUseID: "tool-detail", Operation: intake.Operation{
			CWD: "/repo", EffectiveCWD: "/repo", Command: "echo detail", FilePath: "",
		},
		RawPayload:         []byte(`{"wire":true}`),
		NormalizedJSON:     json.RawMessage(`{"normalized":true}`),
		ClassificationJSON: json.RawMessage(`{"provider":"codex"}`),
		EnvFingerprint:     map[string]string{"CODEX_THREAD_ID": "thread-detail"},
	}
}

func fullDetailPolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{
		Profile: config.AuditStorageProfileFull,
		Detail: config.AuditStorageDetailPolicy{
			WireInput: true, NormalizedInput: true, ProviderEvidence: true,
			EnvironmentEvidence: true, EvaluationContent: true,
		},
	}
}

func minimalDetailPolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{Profile: config.AuditStorageProfileMinimal}
}

func assertDetailRecord(
	t *testing.T,
	store *intake.Store,
	receiptID int64,
	want intake.Record,
) {
	t.Helper()
	got, err := store.GetReceipt(t.Context(), receiptID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	assertRecordDetailEqual(t, got, want)
}

func assertRecordDetailEqual(t *testing.T, got intake.Record, want intake.Record) {
	t.Helper()
	if !reflect.DeepEqual(got.RawPayload, want.RawPayload) ||
		!reflect.DeepEqual(got.NormalizedJSON, want.NormalizedJSON) ||
		!reflect.DeepEqual(got.ClassificationJSON, want.ClassificationJSON) ||
		!reflect.DeepEqual(got.EnvFingerprint, want.EnvFingerprint) {
		t.Fatalf("record detail = %#v, want %#v", got, want)
	}
}

func assertDetailRows(
	t *testing.T,
	database *sql.DB,
	eventID string,
	wantState auditstorage.DetailState,
) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `
		select detail_class from intake_event_details
		where event_id = ? order by detail_class
	`, eventID)
	if err != nil {
		t.Fatalf("query intake detail rows: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var classes []string
	for rows.Next() {
		var class string
		if err := rows.Scan(&class); err != nil {
			t.Fatalf("scan intake detail class: %v", err)
		}
		classes = append(classes, class)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate intake detail classes: %v", err)
	}
	if !reflect.DeepEqual(classes, intakeDetailClasses) {
		t.Fatalf("detail classes = %v, want %v", classes, intakeDetailClasses)
	}
	var recordedClasses string
	var availableClasses string
	var state auditstorage.DetailState
	var stateChangedAt string
	if err := database.QueryRowContext(t.Context(), `
		select recorded_classes_json, available_classes_json, state, state_changed_at
		from intake_event_detail_manifest where event_id = ?
	`, eventID).Scan(&recordedClasses, &availableClasses, &state, &stateChangedAt); err != nil {
		t.Fatalf("query intake detail manifest: %v", err)
	}
	wantClasses := `["wire_input","normalized_input","provider_evidence","environment_evidence"]`
	if recordedClasses != wantClasses || availableClasses != wantClasses ||
		state != wantState || stateChangedAt == "" {
		t.Fatalf("detail manifest = (%s, %s, %q, %q)", recordedClasses, availableClasses, state, stateChangedAt)
	}
}

func assertDetailTableCount(t *testing.T, database *sql.DB, table string, want int) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(
		t.Context(), "select count(*) from "+table,
	).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if count != want {
		t.Fatalf("%s count = %d, want %d", table, count, want)
	}
}
