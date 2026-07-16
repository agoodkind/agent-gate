package evaluation_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/evaluation"
)

const costLayerSchema = `create table gate_evaluation_layers (
	evaluation_id text not null,
	layer_index integer not null,
	kind text not null,
	model_name text not null default '',
	metadata_json blob not null default '{}',
	completed_at text not null,
	cache_status text not null default '',
	cache_key_hash text not null default '',
	primary key(evaluation_id, layer_index)
)`

type costLayerRow struct {
	evaluationID string
	layerIndex   int
	kind         string
	model        string
	metadata     string
	completedAt  string
	cacheStatus  string
	cacheKey     string
}

// presentTokenMetadata renders the v2 layer metadata the daemon records when the
// backend returns usage, matching the snake_case protojson raw keys in live data.
func presentTokenMetadata(requestID string, promptTokens, completionTokens int64) string {
	return fmt.Sprintf(`{"schema_version":2,"upstream_metadata":{"source":"inference_reply",`+
		`"trust":"untrusted","status":"present","raw":{"request_id":%q,`+
		`"prompt_tokens":"%d","completion_tokens":"%d","total_tokens":"%d"}}}`,
		requestID, promptTokens, completionTokens, promptTokens+completionTokens)
}

func absentTokenMetadata() string {
	return `{"schema_version":2,"upstream_metadata":{"source":"inference_reply","trust":"untrusted","status":"absent"}}`
}

func newCostFixture(t *testing.T, rows []costLayerRow) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	defer func() {
		_ = database.Close()
	}()
	if _, err := database.Exec(costLayerSchema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	for _, row := range rows {
		if _, err := database.Exec(`insert into gate_evaluation_layers
			(evaluation_id, layer_index, kind, model_name, metadata_json, completed_at, cache_status, cache_key_hash)
			values (?, ?, ?, ?, ?, ?, ?, ?)`,
			row.evaluationID, row.layerIndex, row.kind, row.model, row.metadata,
			row.completedAt, row.cacheStatus, row.cacheKey,
		); err != nil {
			t.Fatalf("insert fixture row: %v", err)
		}
	}
	return path
}

func costPricing() map[string]evaluation.ModelPricing {
	return map[string]evaluation.ModelPricing{
		"gpt-5.4-mini":                  {InputPerMillion: 0.15, CachedInputPerMillion: 0.015, OutputPerMillion: 0.60},
		"agentgate/agent-gate-judge-v4": {},
	}
}

func TestCostReportDeduplicatesBatchCallsAndPrices(t *testing.T) {
	day := "2026-07-11"
	rows := []costLayerRow{
		// One mini batch call copied across two rule layers: must bill once.
		{"eval-1", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("req-mini-1", 3508, 202), day + "T01:00:00Z", "miss", "ckh-mini-1"},
		{"eval-1", 2, "inference", "gpt-5.4-mini", presentTokenMetadata("req-mini-1", 3508, 202), day + "T01:00:00Z", "miss", "ckh-mini-1"},
		// A distinct mini call the same day.
		{"eval-2", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("req-mini-2", 1000, 100), day + "T02:00:00Z", "miss", "ckh-mini-2"},
		// A free v4 call.
		{"eval-2", 2, "inference", "agentgate/agent-gate-judge-v4", presentTokenMetadata("req-v4-1", 471, 1), day + "T02:00:01Z", "", ""},
		// Absent-token layer contributes no cost and no call.
		{"eval-3", 1, "inference", "gpt-5.4-mini", absentTokenMetadata(), day + "T03:00:00Z", "", ""},
	}
	path := newCostFixture(t, rows)

	result, err := evaluation.CostReport(context.Background(), path, costPricing(), evaluation.CostFilter{})
	if err != nil {
		t.Fatalf("CostReport: %v", err)
	}

	mini := findModelCost(t, result, "gpt-5.4-mini")
	if mini.Calls != 2 {
		t.Fatalf("mini calls = %d, want 2 (batch copy deduped)", mini.Calls)
	}
	if mini.PromptTokens != 4508 || mini.CompletionTokens != 302 {
		t.Fatalf("mini tokens = prompt %d completion %d, want 4508/302", mini.PromptTokens, mini.CompletionTokens)
	}
	// (3508*0.15+202*0.60) + (1000*0.15+100*0.60) = 647 + 210 = 857 micros.
	if mini.EstimatedCostMicros != 857 {
		t.Fatalf("mini cost = %d micros, want 857", mini.EstimatedCostMicros)
	}
	if !mini.Priced {
		t.Fatalf("mini should be priced")
	}

	v4 := findModelCost(t, result, "agentgate/agent-gate-judge-v4")
	if v4.Calls != 1 || v4.EstimatedCostMicros != 0 {
		t.Fatalf("v4 = calls %d cost %d, want 1 call and free", v4.Calls, v4.EstimatedCostMicros)
	}

	if result.TotalBilledCostMicros != 857 {
		t.Fatalf("total billed = %d, want 857", result.TotalBilledCostMicros)
	}
	if result.CachedTokensAvailable {
		t.Fatalf("cached tokens should be unavailable in current data")
	}
}

func TestCostReportDedupCacheStats(t *testing.T) {
	day := "2026-07-11"
	rows := []costLayerRow{
		// A batch decision with a hit reused across two layers: count once.
		{"eval-1", 1, "inference", "gpt-5.4-mini", absentTokenMetadata(), day + "T01:00:00Z", "hit", "ckh-1"},
		{"eval-1", 2, "inference", "gpt-5.4-mini", absentTokenMetadata(), day + "T01:00:00Z", "hit", "ckh-1"},
		// Two distinct misses.
		{"eval-2", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("r2", 10, 1), day + "T02:00:00Z", "miss", "ckh-2"},
		{"eval-3", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("r3", 10, 1), day + "T03:00:00Z", "miss", "ckh-3"},
	}
	path := newCostFixture(t, rows)
	result, err := evaluation.CostReport(context.Background(), path, costPricing(), evaluation.CostFilter{})
	if err != nil {
		t.Fatalf("CostReport: %v", err)
	}
	if result.DedupCache.Hits != 1 || result.DedupCache.Misses != 2 {
		t.Fatalf("dedup cache = %+v, want 1 hit 2 miss", result.DedupCache)
	}
	if rate := result.DedupCache.HitRate(); rate < 0.33 || rate > 0.34 {
		t.Fatalf("hit rate = %v, want ~0.333", rate)
	}
}

func TestCostReportAppliesWindowFilter(t *testing.T) {
	rows := []costLayerRow{
		{"eval-in", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("in", 1000, 0), "2026-07-11T12:00:00Z", "miss", "ckh-in"},
		{"eval-out", 1, "inference", "gpt-5.4-mini", presentTokenMetadata("out", 5000, 0), "2026-07-20T12:00:00Z", "miss", "ckh-out"},
	}
	path := newCostFixture(t, rows)
	filter := evaluation.CostFilter{
		Since: time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC),
	}
	result, err := evaluation.CostReport(context.Background(), path, costPricing(), filter)
	if err != nil {
		t.Fatalf("CostReport: %v", err)
	}
	mini := findModelCost(t, result, "gpt-5.4-mini")
	if mini.Calls != 1 || mini.PromptTokens != 1000 {
		t.Fatalf("windowed mini = calls %d prompt %d, want 1 call 1000 tokens", mini.Calls, mini.PromptTokens)
	}
	// One day window, 150 micros → projected 30x = 4500 micros/month.
	if result.WindowDays != 1 {
		t.Fatalf("window days = %v, want 1", result.WindowDays)
	}
	if result.ProjectedMonthlyMicros != 4500 {
		t.Fatalf("projected monthly = %d, want 4500", result.ProjectedMonthlyMicros)
	}
}

func TestCostReportHandlesMissingHistory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.db")
	result, err := evaluation.CostReport(context.Background(), missing, costPricing(), evaluation.CostFilter{})
	if err != nil {
		t.Fatalf("CostReport missing db: %v", err)
	}
	if len(result.Models) != 0 || result.Note == "" {
		t.Fatalf("missing db result = %+v, want empty with note", result)
	}
}

func findModelCost(t *testing.T, result evaluation.CostReportResult, model string) evaluation.ModelCost {
	t.Helper()
	for _, cost := range result.Models {
		if cost.Model == model {
			return cost
		}
	}
	t.Fatalf("model %q missing from report models %+v", model, result.Models)
	return evaluation.ModelCost{}
}
