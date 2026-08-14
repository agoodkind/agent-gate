package evaluation_test

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestRecordCompletedCommitsEvaluationSummaryAndDetail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	intakeStore, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := intakeStore.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	receipt, err := intakeStore.Append(t.Context(), intake.Record{
		EventID: "event-summary-detail", System: "codex", EventName: "PreToolUse",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	store := intakeStore.Evaluations()
	record := completeRecord(receipt)
	record.Layers[1].MetadataJSON = costLayerMetadata(
		"cost-rule", "request-summary", "requested-summary", 41, 7, 11,
	)

	if err := store.RecordCompleted(t.Context(), record); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	assertEvaluationSummary(t, intakeStore.Handle(), record.Evaluation.EvaluationID)
	assertEvaluationDetail(t, intakeStore.Handle(), record)

	got, err := store.Get(t.Context(), record.Evaluation.EvaluationID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("round trip mismatch\ngot:  %#v\nwant: %#v", got, record)
	}
}

func TestCostReportSurvivesEvaluationDetailRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "event-cost-detail", System: "codex", EventName: "PreToolUse",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	record := completeRecord(receipt)
	record.Evaluation.EvaluationID = "evaluation-cost-detail"
	record.Layers[1].ModelName = "gpt-5.4-mini"
	record.Layers[1].CacheStatus = "miss"
	record.Layers[1].CacheKeyHash = "cache-cost-detail"
	record.Layers[1].MetadataJSON = costLayerMetadata(
		"cost-rule", "request-cost-detail", "gpt-5.4-mini", 1000, 200, 100,
	)
	if err := store.Evaluations().RecordCompleted(t.Context(), record); err != nil {
		t.Fatalf("RecordCompleted: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `
		delete from gate_evaluation_label_details;
		delete from gate_evaluation_layer_details;
		delete from gate_evaluation_details;
	`); err != nil {
		t.Fatalf("delete evaluation detail: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	result, err := evaluation.CostReport(
		t.Context(),
		path,
		map[string]evaluation.ModelPricing{
			"gpt-5.4-mini": {
				InputPerMillion: 0.15, CachedInputPerMillion: 0.015,
				OutputPerMillion: 0.60,
			},
		},
		evaluation.CostFilter{},
	)
	if err != nil {
		t.Fatalf("CostReport: %v", err)
	}
	cost := findModelCost(t, result, "gpt-5.4-mini")
	if cost.Calls != 1 || cost.PromptTokens != 1000 || cost.CachedTokens != 200 ||
		cost.CompletionTokens != 100 || cost.EstimatedCostMicros != 183 {
		t.Fatalf("cost after detail removal = %+v", cost)
	}
}

func assertEvaluationSummary(t *testing.T, database *sql.DB, evaluationID string) {
	t.Helper()
	var detailState string
	if err := database.QueryRowContext(t.Context(), `
		select detail_state from gate_evaluations where evaluation_id = ?
	`, evaluationID).Scan(&detailState); err != nil {
		t.Fatalf("read detail state: %v", err)
	}
	if detailState != "available" {
		t.Fatalf("detail state = %q, want available", detailState)
	}
	var status string
	var requestID string
	var requestedModel string
	var promptTokens int64
	var cachedTokens int64
	var completionTokens int64
	var ruleName string
	if err := database.QueryRowContext(t.Context(), `
		select upstream_metadata_status, request_id, requested_model,
			prompt_tokens, cached_tokens, completion_tokens, rule_name
		from gate_evaluation_layers where evaluation_id = ? and layer_index = 1
	`, evaluationID).Scan(
		&status, &requestID, &requestedModel,
		&promptTokens, &cachedTokens, &completionTokens, &ruleName,
	); err != nil {
		t.Fatalf("read layer summary: %v", err)
	}
	if status != "present" || requestID != "request-summary" ||
		requestedModel != "requested-summary" || promptTokens != 41 ||
		cachedTokens != 7 || completionTokens != 11 || ruleName != "cost-rule" {
		t.Fatalf(
			"layer summary = status %q request %q model %q tokens %d/%d/%d rule %q",
			status, requestID, requestedModel, promptTokens, cachedTokens,
			completionTokens, ruleName,
		)
	}
}

func assertEvaluationDetail(t *testing.T, database *sql.DB, record evaluation.Record) {
	t.Helper()
	var evaluationError []byte
	if err := database.QueryRowContext(t.Context(), `
		select error_json from gate_evaluation_details where evaluation_id = ?
	`, record.Evaluation.EvaluationID).Scan(&evaluationError); err != nil {
		t.Fatalf("read evaluation detail: %v", err)
	}
	if string(evaluationError) != string(record.Evaluation.ErrorJSON) {
		t.Fatalf("evaluation error = %s, want %s", evaluationError, record.Evaluation.ErrorJSON)
	}
	var inputJSON []byte
	var outputJSON []byte
	var metadataJSON []byte
	var errorMessage string
	if err := database.QueryRowContext(t.Context(), `
		select input_json, output_json, metadata_json, error_message
		from gate_evaluation_layer_details
		where evaluation_id = ? and layer_index = 1
	`, record.Evaluation.EvaluationID).Scan(
		&inputJSON, &outputJSON, &metadataJSON, &errorMessage,
	); err != nil {
		t.Fatalf("read layer detail: %v", err)
	}
	if string(inputJSON) != string(record.Layers[1].InputJSON) ||
		string(outputJSON) != string(record.Layers[1].OutputJSON) ||
		string(metadataJSON) != string(record.Layers[1].MetadataJSON) ||
		errorMessage != record.Layers[1].ErrorMessage {
		t.Fatalf("layer detail = %s %s %s %q", inputJSON, outputJSON, metadataJSON, errorMessage)
	}
	var rationale string
	if err := database.QueryRowContext(t.Context(), `
		select rationale from gate_evaluation_label_details
		where evaluation_id = ? and namespace = ? and label_version = ?
	`, record.Evaluation.EvaluationID, record.Labels[0].Namespace,
		record.Labels[0].LabelVersion).Scan(&rationale); err != nil {
		t.Fatalf("read label detail: %v", err)
	}
	if rationale != record.Labels[0].Rationale {
		t.Fatalf("rationale = %q, want %q", rationale, record.Labels[0].Rationale)
	}
}

func costLayerMetadata(
	ruleName string,
	requestID string,
	requestedModel string,
	promptTokens int64,
	cachedTokens int64,
	completionTokens int64,
) json.RawMessage {
	return json.RawMessage(`{"schema_version":2,"rule_name":"` + ruleName +
		`","verified_provenance":{"requested_model":"` + requestedModel +
		`","reported_prompt_hash_status":"absent","reported_schema_hash_status":"absent"},` +
		`"upstream_metadata":{"source":"inference_reply","trust":"untrusted",` +
		`"status":"present","raw":{"request_id":"` + requestID +
		`","requested_model":"` + requestedModel + `","prompt_tokens":"` +
		jsonInt(promptTokens) + `","cached_tokens":"` + jsonInt(cachedTokens) +
		`","completion_tokens":"` + jsonInt(completionTokens) + `"}}}`)
}

func jsonInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
