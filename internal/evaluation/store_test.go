package evaluation_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestStoreRoundTripsCompletedEvaluation(t *testing.T) {
	store, receipt := newEvaluationStore(t)
	record := completeRecord(receipt)

	if err := store.RecordCompleted(context.Background(), record); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	got, err := store.Get(context.Background(), record.Evaluation.EvaluationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, record)
	}
}

func TestStoreMigratesPopulatedLayerMetadata(t *testing.T) {
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
	receipt, err := intakeStore.Append(ctx, intake.Record{
		EventID:        "evt-legacy-evaluation",
		System:         "codex",
		SessionID:      "session-legacy",
		EventName:      "PreToolUse",
		RawPayload:     []byte(`{"command":"make test"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make test"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	record := completeRecord(receipt)
	record.Evaluation.EvaluationID = "eval-legacy"
	if err := intakeStore.Evaluations().RecordCompleted(ctx, record); err != nil {
		t.Fatalf("RecordCompleted legacy row: %v", err)
	}
	if _, err := intakeStore.Handle().ExecContext(
		ctx,
		`alter table gate_evaluation_layers drop column metadata_json`,
	); err != nil {
		t.Fatalf("remove post-Task-6 metadata column: %v", err)
	}

	migratedStore, err := evaluation.NewStore(ctx, intakeStore.Handle())
	if err != nil {
		t.Fatalf("NewStore migration: %v", err)
	}
	var metadataJSON []byte
	err = intakeStore.Handle().QueryRowContext(ctx, `
		select metadata_json
		from gate_evaluation_layers
		where evaluation_id = ? and layer_index = 0
	`, record.Evaluation.EvaluationID).Scan(&metadataJSON)
	if err != nil {
		t.Fatalf("read migrated metadata: %v", err)
	}
	if string(metadataJSON) != "{}" {
		t.Fatalf("migrated metadata = %q, want %q", metadataJSON, "{}")
	}
	got, err := migratedStore.Get(ctx, record.Evaluation.EvaluationID)
	if err != nil {
		t.Fatalf("Get migrated evaluation: %v", err)
	}
	for _, layer := range got.Layers {
		if string(layer.MetadataJSON) != "{}" {
			t.Fatalf("layer %d metadata = %q, want %q", layer.LayerIndex, layer.MetadataJSON, "{}")
		}
	}
}

func TestStoreRollsBackCompleteRecordOnLateFailure(t *testing.T) {
	store, receipt := newEvaluationStore(t)
	record := completeRecord(receipt)
	record.Labels = append(record.Labels, record.Labels[0])

	err := store.RecordCompleted(context.Background(), record)
	if err == nil {
		t.Fatal("RecordCompleted succeeded with duplicate label identity")
	}
	_, err = store.Get(context.Background(), record.Evaluation.EvaluationID)
	if !errors.Is(err, evaluation.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestStoreRejectsInvalidJSONBeforeWriting(t *testing.T) {
	store, receipt := newEvaluationStore(t)
	tests := []struct {
		name   string
		mutate func(*evaluation.Record)
	}{
		{
			name: "evaluation error",
			mutate: func(record *evaluation.Record) {
				record.Evaluation.ErrorJSON = json.RawMessage(`{"broken"`)
			},
		},
		{
			name: "layer input",
			mutate: func(record *evaluation.Record) {
				record.Layers[0].InputJSON = json.RawMessage(`{"broken"`)
			},
		},
		{
			name: "layer output",
			mutate: func(record *evaluation.Record) {
				record.Layers[0].OutputJSON = json.RawMessage(`{"broken"`)
			},
		},
		{
			name: "layer metadata",
			mutate: func(record *evaluation.Record) {
				record.Layers[0].MetadataJSON = json.RawMessage(`{"broken"`)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := completeRecord(receipt)
			record.Evaluation.EvaluationID = "invalid-" + test.name
			test.mutate(&record)
			if err := store.RecordCompleted(context.Background(), record); err == nil {
				t.Fatal("RecordCompleted accepted invalid JSON")
			}
			_, err := store.Get(context.Background(), record.Evaluation.EvaluationID)
			if !errors.Is(err, evaluation.ErrNotFound) {
				t.Fatalf("Get error = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestStoreRejectsInvalidLabelConfidence(t *testing.T) {
	store, receipt := newEvaluationStore(t)
	for _, confidence := range []float64{-0.01, 1.01, math.NaN(), math.Inf(1)} {
		record := completeRecord(receipt)
		record.Evaluation.EvaluationID = "invalid-confidence"
		record.Labels[0].Confidence = &confidence
		if err := store.RecordCompleted(context.Background(), record); err == nil {
			t.Fatalf("RecordCompleted accepted confidence %v", confidence)
		}
	}
}

func TestNewStoreEnablesForeignKeyEnforcement(t *testing.T) {
	database, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "evaluation.db"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close database: %v", err)
		}
	})
	_, err = database.ExecContext(context.Background(), `
		create table intake_events (event_id text primary key);
		create table intake_receipts (
			receipt_id integer primary key,
			event_id text not null,
			unique(receipt_id, event_id)
		);
	`)
	if err != nil {
		t.Fatalf("create intake schema: %v", err)
	}
	if _, err := evaluation.NewStore(context.Background(), database); err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var enabled int
	if err := database.QueryRowContext(context.Background(), "pragma foreign_keys").Scan(&enabled); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if enabled != 1 {
		t.Fatalf("foreign_keys = %d, want 1", enabled)
	}
}

func TestStoreRejectsMismatchedReceiptEvent(t *testing.T) {
	store, receipt := newEvaluationStore(t)
	record := completeRecord(receipt)
	record.Evaluation.EventID = "different-event"

	if err := store.RecordCompleted(context.Background(), record); err == nil {
		t.Fatal("RecordCompleted accepted a receipt from a different event")
	}
	_, err := store.Get(context.Background(), record.Evaluation.EvaluationID)
	if !errors.Is(err, evaluation.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}

func TestStoreSchemaHasForeignKeysAndIndices(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	intakeStore, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := intakeStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})

	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open schema database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Fatalf("close schema database: %v", err)
		}
	})

	assertForeignKey(t, database, "gate_evaluations", "intake_receipts")
	assertForeignKey(t, database, "gate_evaluation_layers", "gate_evaluations")
	assertForeignKey(t, database, "gate_evaluation_layers", "gate_evaluation_layers")
	assertForeignKey(t, database, "gate_evaluation_labels", "gate_evaluations")
	assertIndex(t, database, "gate_evaluations", "gate_evaluations_event_id_idx")
	assertIndex(t, database, "gate_evaluations", "gate_evaluations_receipt_id_idx")
	assertIndex(t, database, "gate_evaluation_layers", "gate_evaluation_layers_kind_name_idx")
	assertIndex(t, database, "gate_evaluation_labels", "gate_evaluation_labels_verdict_idx")
}

func newEvaluationStore(t *testing.T) (*evaluation.Store, intake.AppendResult) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	intakeStore, err := intake.OpenSQLite(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := intakeStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	})
	receipt, err := intakeStore.Append(context.Background(), intake.Record{
		EventID:        "evt-evaluation",
		RecordedAt:     time.Date(2026, 7, 10, 1, 2, 3, 4, time.UTC),
		System:         "codex",
		SessionID:      "session-1",
		TurnID:         "turn-2",
		EventName:      "PreToolUse",
		ToolName:       "exec_command",
		ToolUseID:      "tool-3",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	return intakeStore.Evaluations(), receipt
}

func completeRecord(receipt intake.AppendResult) evaluation.Record {
	confidence := 0.875
	parentIndex := 0
	cacheVersion := int64(7)
	cacheExpiry := time.Date(2026, 7, 10, 2, 30, 0, 0, time.UTC)
	return evaluation.Record{
		Evaluation: evaluation.Evaluation{
			EvaluationID:      "eval-1",
			ReceiptID:         receipt.ReceiptID,
			EventID:           receipt.EventID,
			Attempt:           2,
			Mode:              "hot",
			ConfigHash:        "sha256:config",
			EngineVersion:     "v6",
			EngineCommit:      "abc123",
			EngineBuildHash:   "sha256:build",
			InputHash:         "sha256:input",
			StartedAt:         time.Date(2026, 7, 10, 1, 2, 4, 0, time.UTC),
			CompletedAt:       time.Date(2026, 7, 10, 1, 2, 5, 0, time.UTC),
			FinalVerdict:      "block",
			FinalSource:       "model",
			EnforcementAction: "deny",
			Enforced:          true,
			TotalLatencyUS:    1_000_000,
			ErrorJSON:         json.RawMessage(`{"code":"degraded","retryable":false}`),
		},
		Layers: []evaluation.Layer{
			{
				LayerIndex:     0,
				Kind:           "oracle",
				Name:           "deterministic",
				Status:         "complete",
				InputReference: "normalized_payload",
				InputJSON:      json.RawMessage(`{"command":"make check"}`),
				InputHash:      "sha256:layer-input-0",
				OutputHash:     "sha256:layer-output-0",
				OutputJSON:     json.RawMessage(`{"verdict":"allow"}`),
				MetadataJSON:   json.RawMessage(`{"schema_version":1,"rule_name":"deterministic"}`),
				StartedAt:      time.Date(2026, 7, 10, 1, 2, 4, 0, time.UTC),
				CompletedAt:    time.Date(2026, 7, 10, 1, 2, 4, 200_000_000, time.UTC),
				LatencyUS:      200_000,
				ServiceName:    "agent-gate",
				ServiceVersion: "v6",
				RetryCount:     0,
			},
			{
				LayerIndex:        1,
				ParentLayerIndex:  &parentIndex,
				Kind:              "model",
				Name:              "lm-review",
				Status:            "error",
				InputReference:    "layer:0.output",
				InputJSON:         json.RawMessage(`{"command":"make check","oracle":"allow"}`),
				InputHash:         "sha256:layer-input-1",
				OutputHash:        "sha256:layer-output-1",
				OutputJSON:        json.RawMessage(`{"verdict":"block","confidence":0.875}`),
				MetadataJSON:      json.RawMessage(`{"schema_version":1,"request_id":"request-1"}`),
				StartedAt:         time.Date(2026, 7, 10, 1, 2, 4, 200_000_000, time.UTC),
				CompletedAt:       time.Date(2026, 7, 10, 1, 2, 5, 0, time.UTC),
				LatencyUS:         800_000,
				ServiceName:       "lm-review",
				ServiceVersion:    "2026-07-10",
				ModelName:         "reviewer",
				ModelVersion:      "v3",
				PromptHash:        "sha256:prompt",
				SchemaHash:        "sha256:schema",
				CacheStatus:       "hit",
				CacheKeyHash:      "sha256:cache-key",
				CacheEntryVersion: &cacheVersion,
				CacheExpiresAt:    &cacheExpiry,
				ErrorCode:         "timeout",
				ErrorMessage:      "upstream deadline exceeded",
				RetryCount:        3,
			},
		},
		Labels: []evaluation.Label{
			{
				Namespace:    "human-review",
				LabelVersion: 4,
				Verdict:      "block",
				Source:       "reviewer@example.invalid",
				Confidence:   &confidence,
				Rationale:    "The command crosses the configured boundary.",
				CreatedAt:    time.Date(2026, 7, 10, 1, 5, 0, 0, time.UTC),
			},
		},
	}
}

func assertForeignKey(t *testing.T, database *sql.DB, table string, referencedTable string) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), "pragma foreign_key_list("+table+")")
	if err != nil {
		t.Fatalf("foreign keys for %s: %v", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	found := false
	for rows.Next() {
		var id int
		var sequence int
		var target string
		var from string
		var to string
		var onUpdate string
		var onDelete string
		var match string
		if err := rows.Scan(&id, &sequence, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			t.Fatalf("scan foreign key for %s: %v", table, err)
		}
		if target == referencedTable {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate foreign keys for %s: %v", table, err)
	}
	if !found {
		t.Fatalf("%s has no foreign key to %s", table, referencedTable)
	}
}

func assertIndex(t *testing.T, database *sql.DB, table string, index string) {
	t.Helper()
	rows, err := database.QueryContext(context.Background(), "pragma index_list("+table+")")
	if err != nil {
		t.Fatalf("indices for %s: %v", table, err)
	}
	defer func() {
		_ = rows.Close()
	}()
	found := false
	for rows.Next() {
		var sequence int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan index for %s: %v", table, err)
		}
		if name == index {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate indices for %s: %v", table, err)
	}
	if !found {
		t.Fatalf("%s has no %s index", table, index)
	}
}
