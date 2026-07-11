package evaluation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestStoreListFiltersJoinedEvaluations(t *testing.T) {
	store, _, path, first, second := newEvaluationQueryFixture(t)
	ctx := context.Background()

	tests := []struct {
		name   string
		filter evaluation.QueryFilter
		wantID string
	}{
		{name: "evaluation id", filter: evaluation.QueryFilter{EvaluationID: first.Evaluation.EvaluationID}, wantID: first.Evaluation.EvaluationID},
		{name: "event id", filter: evaluation.QueryFilter{EventID: first.Evaluation.EventID}, wantID: first.Evaluation.EvaluationID},
		{name: "receipt id", filter: evaluation.QueryFilter{ReceiptID: first.Evaluation.ReceiptID}, wantID: first.Evaluation.EvaluationID},
		{name: "mode", filter: evaluation.QueryFilter{Mode: "deferred"}, wantID: second.Evaluation.EvaluationID},
		{name: "since", filter: evaluation.QueryFilter{Since: second.Evaluation.CompletedAt.Add(-time.Second)}, wantID: second.Evaluation.EvaluationID},
		{name: "until", filter: evaluation.QueryFilter{Until: first.Evaluation.CompletedAt.Add(time.Second)}, wantID: first.Evaluation.EvaluationID},
		{name: "system", filter: evaluation.QueryFilter{System: "codex"}, wantID: first.Evaluation.EvaluationID},
		{name: "session", filter: evaluation.QueryFilter{SessionID: "session-2"}, wantID: second.Evaluation.EvaluationID},
		{name: "event", filter: evaluation.QueryFilter{EventName: "PostToolUse"}, wantID: second.Evaluation.EvaluationID},
		{name: "tool", filter: evaluation.QueryFilter{ToolName: "Bash"}, wantID: second.Evaluation.EvaluationID},
		{name: "rule", filter: evaluation.QueryFilter{RuleName: "review-rule"}, wantID: first.Evaluation.EvaluationID},
		{name: "static rule", filter: evaluation.QueryFilter{RuleName: "static-rule"}, wantID: first.Evaluation.EvaluationID},
		{name: "layer name", filter: evaluation.QueryFilter{LayerName: "review-layer"}, wantID: first.Evaluation.EvaluationID},
		{name: "layer kind", filter: evaluation.QueryFilter{LayerKind: "inference"}, wantID: first.Evaluation.EvaluationID},
		{name: "layer outcome", filter: evaluation.QueryFilter{LayerOutcome: "match"}, wantID: first.Evaluation.EvaluationID},
		{name: "model", filter: evaluation.QueryFilter{ModelName: "gpt-test"}, wantID: first.Evaluation.EvaluationID},
		{name: "final verdict", filter: evaluation.QueryFilter{FinalVerdict: "allow"}, wantID: second.Evaluation.EvaluationID},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			records, err := store.List(ctx, test.filter)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(records) != 1 || records[0].EvaluationID != test.wantID {
				t.Fatalf("records = %+v, want only %q", records, test.wantID)
			}
		})
	}

	result, err := evaluation.Query(ctx, path, evaluation.QueryFilter{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("Query pagination: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].EvaluationID != first.Evaluation.EvaluationID {
		t.Fatalf("paginated records = %+v, want %q", result.Records, first.Evaluation.EvaluationID)
	}
}

func TestStoreListReturnsOrderedSafeTrainingExport(t *testing.T) {
	store, _, _, first, _ := newEvaluationQueryFixture(t)

	records, err := store.List(context.Background(), evaluation.QueryFilter{
		EvaluationID: first.Evaluation.EvaluationID,
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	record := records[0]
	if record.System != "codex" || record.SessionID != "session-1" ||
		record.EventName != "PreToolUse" || record.ToolName != "exec_command" {
		t.Fatalf("joined intake metadata = %+v", record)
	}
	if len(record.Layers) != 2 || record.Layers[0].LayerIndex != 0 ||
		record.Layers[1].LayerIndex != 1 || record.Layers[1].ParentLayerIndex == nil ||
		*record.Layers[1].ParentLayerIndex != 0 {
		t.Fatalf("ordered layers = %+v", record.Layers)
	}
	if len(record.Labels) != 2 || record.Labels[0].Namespace != "alpha" ||
		record.Labels[1].Namespace != "zeta" {
		t.Fatalf("ordered labels = %+v", record.Labels)
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(encoded)
	for _, prohibited := range []string{
		"input_json", "selected input secret", "error_message", "backend secret",
		"rationale", "authorization",
	} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(prohibited)) {
			t.Fatalf("training export contains prohibited %q: %s", prohibited, text)
		}
	}
	for _, required := range []string{
		`"verified_provenance":{"requested_model":"gpt-test"}`,
		`"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"present","raw":{"prompt_tokens":"0"}}`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("training export missing %s: %s", required, text)
		}
	}
	if strings.Contains(text, "completion_tokens") {
		t.Fatalf("absent optional token field was invented: %s", text)
	}
}

func TestQueryHandlesMissingAndLegacyEvaluationHistory(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "missing.db")
	missing, err := evaluation.Query(context.Background(), missingPath, evaluation.QueryFilter{})
	if err != nil {
		t.Fatalf("Query missing database: %v", err)
	}
	if len(missing.Records) != 0 || !strings.Contains(missing.Note, "no evaluation history") {
		t.Fatalf("missing result = %+v", missing)
	}

	legacyPath := filepath.Join(t.TempDir(), "legacy.db")
	database, err := sql.Open("sqlite3", legacyPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := database.Exec(`create table intake_events (event_id text primary key)`); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}
	legacy, err := evaluation.Query(context.Background(), legacyPath, evaluation.QueryFilter{})
	if err != nil {
		t.Fatalf("Query legacy database: %v", err)
	}
	if len(legacy.Records) != 0 || !strings.Contains(legacy.Note, "no evaluation history") {
		t.Fatalf("legacy result = %+v", legacy)
	}
}

func TestStoreListRejectsMissingAndCorruptChildRows(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, string)
	}{
		{
			name: "missing final layer",
			mutate: func(t *testing.T, database *sql.DB, evaluationID string) {
				t.Helper()
				if _, err := database.Exec(`delete from gate_evaluation_layers where evaluation_id = ? and layer_index = 1`, evaluationID); err != nil {
					t.Fatalf("delete final layer: %v", err)
				}
			},
		},
		{
			name: "missing label",
			mutate: func(t *testing.T, database *sql.DB, evaluationID string) {
				t.Helper()
				if _, err := database.Exec(`delete from gate_evaluation_labels where evaluation_id = ? and namespace = 'zeta'`, evaluationID); err != nil {
					t.Fatalf("delete label: %v", err)
				}
			},
		},
		{
			name: "corrupt metadata",
			mutate: func(t *testing.T, database *sql.DB, evaluationID string) {
				t.Helper()
				if _, err := database.Exec(`update gate_evaluation_layers set metadata_json = '{' where evaluation_id = ? and layer_index = 1`, evaluationID); err != nil {
					t.Fatalf("corrupt metadata: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, database, _, first, _ := newEvaluationQueryFixture(t)
			test.mutate(t, database, first.Evaluation.EvaluationID)
			_, err := store.List(context.Background(), evaluation.QueryFilter{
				EvaluationID: first.Evaluation.EvaluationID,
			})
			if err == nil {
				t.Fatal("List accepted incomplete or corrupt child rows")
			}
		})
	}
}

func TestStoreMigratesEvaluationQueryColumns(t *testing.T) {
	_, database, _, first, _ := newEvaluationQueryFixture(t)
	if _, err := database.Exec(`drop index gate_evaluation_layers_outcome_idx`); err != nil {
		t.Fatalf("drop outcome index: %v", err)
	}
	if _, err := database.Exec(`alter table gate_evaluation_layers drop column outcome`); err != nil {
		t.Fatalf("drop outcome column: %v", err)
	}
	if _, err := database.Exec(`alter table gate_evaluations drop column layer_count`); err != nil {
		t.Fatalf("drop layer count column: %v", err)
	}
	if _, err := database.Exec(`alter table gate_evaluations drop column label_count`); err != nil {
		t.Fatalf("drop label count column: %v", err)
	}

	migrated, err := evaluation.NewStore(context.Background(), database)
	if err != nil {
		t.Fatalf("NewStore migration: %v", err)
	}
	got, err := migrated.Get(context.Background(), first.Evaluation.EvaluationID)
	if err != nil {
		t.Fatalf("Get migrated evaluation: %v", err)
	}
	for _, layer := range got.Layers {
		if layer.Outcome != "" {
			t.Fatalf("legacy layer outcome = %q, want empty", layer.Outcome)
		}
	}
	var layerCount int
	var labelCount int
	if err := database.QueryRow(`
		select layer_count, label_count
		from gate_evaluations
		where evaluation_id = ?
	`, first.Evaluation.EvaluationID).Scan(&layerCount, &labelCount); err != nil {
		t.Fatalf("read migrated child counts: %v", err)
	}
	if layerCount != -1 || labelCount != -1 {
		t.Fatalf("migrated child counts = %d, %d; want -1, -1", layerCount, labelCount)
	}
}

func newEvaluationQueryFixture(
	t *testing.T,
) (*evaluation.Store, *sql.DB, string, evaluation.Record, evaluation.Record) {
	t.Helper()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.db")
	intakeStore, err := intake.OpenSQLite(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := intakeStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	firstReceipt, err := intakeStore.Append(ctx, intake.Record{
		EventID: "evt-query-1", RecordedAt: time.Date(2026, 7, 11, 1, 0, 0, 0, time.UTC),
		System: "codex", SessionID: "session-1", EventName: "PreToolUse",
		ToolName: "exec_command", RawPayload: []byte(`{"secret":"raw"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append first: %v", err)
	}
	secondReceipt, err := intakeStore.Append(ctx, intake.Record{
		EventID: "evt-query-2", RecordedAt: time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC),
		System: "claude", SessionID: "session-2", EventName: "PostToolUse",
		ToolName: "Bash", RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append second: %v", err)
	}

	first := completeRecord(firstReceipt)
	first.Evaluation.EvaluationID = "eval-query-1"
	first.Evaluation.Mode = "hot"
	first.Evaluation.CompletedAt = time.Date(2026, 7, 11, 1, 0, 1, 0, time.UTC)
	first.Layers[0].Name = "static-layer"
	first.Layers[0].Outcome = "nonmatch"
	first.Layers[0].MetadataJSON = json.RawMessage(`{
		"schema_version":1,
		"checked_rules":[{"rule_name":"static-rule","status":"complete","matched":false}]
	}`)
	first.Layers[1].Kind = "inference"
	first.Layers[1].Name = "review-layer"
	first.Layers[1].Status = "complete"
	first.Layers[1].Outcome = "match"
	first.Layers[1].ModelName = "gpt-test"
	first.Layers[1].InputJSON = json.RawMessage(`{"input":"selected input secret","authorization":"backend secret"}`)
	first.Layers[1].MetadataJSON = json.RawMessage(`{
		"schema_version":2,
		"rule_name":"review-rule",
		"verified_provenance":{"requested_model":"gpt-test"},
		"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"present","raw":{"prompt_tokens":"0"}}
	}`)
	first.Layers[1].ErrorMessage = "backend secret"
	first.Labels = []evaluation.Label{
		{Namespace: "zeta", LabelVersion: 1, Verdict: "block", Source: "human", Rationale: "authorization", CreatedAt: first.Evaluation.CompletedAt},
		{Namespace: "alpha", LabelVersion: 2, Verdict: "block", Source: "human", Rationale: "authorization", CreatedAt: first.Evaluation.CompletedAt},
	}

	second := completeRecord(secondReceipt)
	second.Evaluation.EvaluationID = "eval-query-2"
	second.Evaluation.Mode = "deferred"
	second.Evaluation.FinalVerdict = "allow"
	second.Evaluation.CompletedAt = time.Date(2026, 7, 11, 2, 0, 1, 0, time.UTC)
	second.Layers = second.Layers[:1]
	second.Layers[0].Name = "deferred-static"
	second.Layers[0].Outcome = "nonmatch"
	second.Labels = nil

	store := intakeStore.Evaluations()
	for _, record := range []evaluation.Record{first, second} {
		if err := store.RecordCompleted(ctx, record); err != nil {
			t.Fatalf("RecordCompleted %s: %v", record.Evaluation.EvaluationID, err)
		}
	}
	return store, intakeStore.Handle(), path, first, second
}

func TestQueryRejectsInvalidLimitAndOffset(t *testing.T) {
	store, _, _, _, _ := newEvaluationQueryFixture(t)
	for _, filter := range []evaluation.QueryFilter{
		{Limit: evaluation.MaxQueryLimit + 1},
		{Offset: -1},
	} {
		_, err := store.List(context.Background(), filter)
		if err == nil || errors.Is(err, evaluation.ErrNotFound) {
			t.Fatalf("List(%+v) error = %v, want validation error", filter, err)
		}
	}
}
