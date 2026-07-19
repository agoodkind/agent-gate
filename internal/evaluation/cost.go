package evaluation

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"time"
)

// daysPerMonth projects an observed spend rate to a monthly figure, the number a
// reader compares against a monthly budget like "under $10/mo". The cost report
// sums integer micro-dollars ($1e-6) so repeated addition does not drift the way
// float dollars would across thousands of judge calls.
const daysPerMonth = 30.0

// ModelPricing is the billed price of one judge model in US dollars per one
// million tokens. The caller supplies it from configuration, keeping this package
// independent of the config table shape.
type ModelPricing struct {
	InputPerMillion       float64
	CachedInputPerMillion float64
	OutputPerMillion      float64
}

// CostFilter narrows the cost report to a completed-at window. A zero bound is
// open, so an empty filter reports over all recorded judge calls.
type CostFilter struct {
	Since time.Time
	Until time.Time
}

// ModelCost is one model's judge spend over the report window. Tokens are summed
// over distinct upstream calls, so a batch call copied across several rule layers
// counts once.
type ModelCost struct {
	Model               string `json:"model"`
	Calls               int64  `json:"calls"`
	PromptTokens        int64  `json:"prompt_tokens"`
	CachedTokens        int64  `json:"cached_tokens"`
	CompletionTokens    int64  `json:"completion_tokens"`
	EstimatedCostMicros int64  `json:"estimated_cost_micros"`
	Priced              bool   `json:"priced"`
}

// DailyCost is one model's judge spend on one UTC calendar day, the granularity a
// reader scans to see whether spend is steady or spiking.
type DailyCost struct {
	Day                 string `json:"day"`
	Model               string `json:"model"`
	Calls               int64  `json:"calls"`
	EstimatedCostMicros int64  `json:"estimated_cost_micros"`
}

// DedupCacheStats counts agent-gate's own dedup-cache outcomes over the window,
// deduplicated by cache key so a batch decision reused across rule layers counts
// once. It is separate from any provider prompt cache.
type DedupCacheStats struct {
	Hits   int64 `json:"hits"`
	Misses int64 `json:"misses"`
}

// HitRate returns the dedup cache-hit fraction in [0,1], or 0 when the cache was
// never consulted so the caller can present "no data" rather than a false zero.
func (s DedupCacheStats) HitRate() float64 {
	total := s.Hits + s.Misses
	if total == 0 {
		return 0
	}
	return float64(s.Hits) / float64(total)
}

// CostReportResult is the queryable judge cost and cache view over a window. The
// billed total and monthly projection cover priced cloud models; the local
// record-only model prices to zero.
type CostReportResult struct {
	Since                  time.Time       `json:"since"`
	Until                  time.Time       `json:"until"`
	Models                 []ModelCost     `json:"models"`
	Daily                  []DailyCost     `json:"daily"`
	TotalBilledCostMicros  int64           `json:"total_billed_cost_micros"`
	WindowDays             float64         `json:"window_days"`
	ProjectedMonthlyMicros int64           `json:"projected_monthly_cost_micros"`
	DedupCache             DedupCacheStats `json:"dedup_cache"`
	CachedTokensAvailable  bool            `json:"cached_tokens_available"`
	Source                 string          `json:"source"`
	Note                   string          `json:"note,omitempty"`
}

// callAggregate is one deduplicated upstream judge call's summed tokens on one
// UTC day, the row the SQL layer emits before Go applies prices.
type callAggregate struct {
	model            string
	day              string
	calls            int64
	promptTokens     int64
	cachedTokens     int64
	completionTokens int64
	earliest         time.Time
	latest           time.Time
}

// estimatedCostMicros returns the estimated billed cost of one judge call in
// micro-dollars. cachedTokens bill at the provider cached-input rate and the
// remaining prompt tokens at the full input rate; when cachedTokens is zero or
// unknown every prompt token bills at the full rate, an upper bound on input cost.
func estimatedCostMicros(promptTokens, cachedTokens, completionTokens int64, price ModelPricing) int64 {
	cached := max(cachedTokens, 0)
	cached = min(cached, promptTokens)
	uncached := promptTokens - cached
	micros := float64(uncached)*price.InputPerMillion +
		float64(cached)*price.CachedInputPerMillion +
		float64(completionTokens)*price.OutputPerMillion
	return int64(math.Round(micros))
}

// CostReport reads recorded judge inference layers from an existing SQLite path
// and returns per-model and per-day estimated cost plus the dedup cache-hit rate,
// without creating or migrating the database. Tokens come from the recorded
// upstream metadata and are deduplicated by upstream request id so a batch call
// copied across rule layers is billed once.
func CostReport(
	ctx context.Context,
	path string,
	pricing map[string]ModelPricing,
	filter CostFilter,
) (CostReportResult, error) {
	result := CostReportResult{
		Since:                  filter.Since,
		Until:                  filter.Until,
		Models:                 make([]ModelCost, 0),
		Daily:                  make([]DailyCost, 0),
		TotalBilledCostMicros:  0,
		WindowDays:             0,
		ProjectedMonthlyMicros: 0,
		DedupCache:             DedupCacheStats{Hits: 0, Misses: 0},
		CachedTokensAvailable:  false,
		Source:                 "sqlite",
		Note:                   "",
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Note = "no evaluation history exists yet"
			return result, nil
		}
		return CostReportResult{}, wrapError("stat evaluation sqlite path", err)
	}
	database, err := sql.Open("sqlite3", queryReadOnlySQLiteDSN(path))
	if err != nil {
		return CostReportResult{}, wrapError("open evaluation sqlite db read-only", err)
	}
	defer func() {
		_ = database.Close()
	}()
	if err := database.PingContext(ctx); err != nil {
		return CostReportResult{}, wrapError("ping evaluation sqlite db read-only", err)
	}
	exists, err := queryTableExists(ctx, database, "gate_evaluation_layers")
	if err != nil {
		return CostReportResult{}, err
	}
	if !exists {
		result.Note = "no evaluation history exists yet"
		return result, nil
	}
	aggregates, err := queryCostAggregates(ctx, database, filter)
	if err != nil {
		return CostReportResult{}, err
	}
	cache, err := queryDedupCacheStats(ctx, database, filter)
	if err != nil {
		return CostReportResult{}, err
	}
	result.DedupCache = cache
	buildCostReport(&result, aggregates, pricing)
	if len(result.Models) == 0 && result.Note == "" {
		result.Note = "no judge calls with recorded token usage in range"
	}
	return result, nil
}

// queryCostAggregates deduplicates present-token inference layers by upstream
// request id, then sums each model's tokens per UTC day. Copied batch layers
// share a request id and collapse to one call.
func queryCostAggregates(
	ctx context.Context,
	database *sql.DB,
	filter CostFilter,
) ([]callAggregate, error) {
	since, until := costWindowArgs(filter)
	const query = `
		with calls as (
			select
				json_extract(metadata_json, '$.upstream_metadata.raw.request_id') as request_id,
				max(coalesce(nullif(model_name, ''), json_extract(metadata_json, '$.upstream_metadata.raw.requested_model'))) as model_name,
				max(cast(json_extract(metadata_json, '$.upstream_metadata.raw.prompt_tokens') as integer)) as prompt_tokens,
				max(cast(json_extract(metadata_json, '$.upstream_metadata.raw.completion_tokens') as integer)) as completion_tokens,
				max(cast(coalesce(json_extract(metadata_json, '$.upstream_metadata.raw.cached_tokens'), 0) as integer)) as cached_tokens,
				min(completed_at) as first_at
			from gate_evaluation_layers
			where kind = 'inference'
				and json_extract(metadata_json, '$.upstream_metadata.status') = 'present'
				and coalesce(json_extract(metadata_json, '$.upstream_metadata.raw.request_id'), '') != ''
				and coalesce(nullif(model_name, ''), json_extract(metadata_json, '$.upstream_metadata.raw.requested_model'), '') != ''
				and (? = '' or completed_at >= ?)
				and (? = '' or completed_at <= ?)
			group by request_id
		)
		select model_name, substr(first_at, 1, 10) as day, count(*) as calls,
			coalesce(sum(prompt_tokens), 0), coalesce(sum(completion_tokens), 0),
			coalesce(sum(cached_tokens), 0), min(first_at), max(first_at)
		from calls
		group by model_name, day
		order by day, model_name
	`
	rows, err := database.QueryContext(ctx, query, since, since, until, until)
	if err != nil {
		return nil, wrapError("query judge cost aggregates", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	aggregates := make([]callAggregate, 0)
	for rows.Next() {
		var aggregate callAggregate
		var earliest string
		var latest string
		if err := rows.Scan(
			&aggregate.model, &aggregate.day, &aggregate.calls,
			&aggregate.promptTokens, &aggregate.completionTokens, &aggregate.cachedTokens,
			&earliest, &latest,
		); err != nil {
			return nil, wrapError("scan judge cost aggregate", err)
		}
		aggregate.earliest, err = parseTime(earliest)
		if err != nil {
			return nil, err
		}
		aggregate.latest, err = parseTime(latest)
		if err != nil {
			return nil, err
		}
		aggregates = append(aggregates, aggregate)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate judge cost aggregates", err)
	}
	return aggregates, nil
}

// queryDedupCacheStats counts agent-gate dedup-cache hits and misses over the
// window, deduplicated by cache key so a decision reused across rule layers of one
// batch counts once.
func queryDedupCacheStats(
	ctx context.Context,
	database *sql.DB,
	filter CostFilter,
) (DedupCacheStats, error) {
	since, until := costWindowArgs(filter)
	const query = `
		select cache_status, count(distinct cache_key_hash)
		from gate_evaluation_layers
		where kind = 'inference'
			and cache_status in ('hit', 'miss')
			and (? = '' or completed_at >= ?)
			and (? = '' or completed_at <= ?)
		group by cache_status
	`
	rows, err := database.QueryContext(ctx, query, since, since, until, until)
	if err != nil {
		return DedupCacheStats{}, wrapError("query judge dedup cache stats", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	var stats DedupCacheStats
	for rows.Next() {
		var status string
		var count int64
		if err := rows.Scan(&status, &count); err != nil {
			return DedupCacheStats{}, wrapError("scan judge dedup cache stats", err)
		}
		if status == "hit" {
			stats.Hits = count
		}
		if status == "miss" {
			stats.Misses = count
		}
	}
	if err := rows.Err(); err != nil {
		return DedupCacheStats{}, wrapError("iterate judge dedup cache stats", err)
	}
	return stats, nil
}

// costWindowArgs renders the completed-at bounds as strings, using empty strings
// as the open-bound sentinel the queries test so the predicate is a constant
// string with bound parameters rather than dynamic SQL.
func costWindowArgs(filter CostFilter) (string, string) {
	since := ""
	if !filter.Since.IsZero() {
		since = formatTime(filter.Since)
	}
	until := ""
	if !filter.Until.IsZero() {
		until = formatTime(filter.Until)
	}
	return since, until
}

// buildCostReport applies prices to the aggregated calls, filling per-model and
// per-day cost, the billed total, and the monthly projection from the observed
// spend rate.
func buildCostReport(
	result *CostReportResult,
	aggregates []callAggregate,
	pricing map[string]ModelPricing,
) {
	models := make(map[string]*ModelCost)
	order := make([]string, 0)
	var earliest time.Time
	var latest time.Time
	for _, aggregate := range aggregates {
		price, priced := pricing[aggregate.model]
		cost := estimatedCostMicros(
			aggregate.promptTokens, aggregate.cachedTokens, aggregate.completionTokens, price,
		)
		result.Daily = append(result.Daily, DailyCost{
			Day: aggregate.day, Model: aggregate.model,
			Calls: aggregate.calls, EstimatedCostMicros: cost,
		})
		summary, ok := models[aggregate.model]
		if !ok {
			summary = &ModelCost{
				Model:               aggregate.model,
				Calls:               0,
				PromptTokens:        0,
				CachedTokens:        0,
				CompletionTokens:    0,
				EstimatedCostMicros: 0,
				Priced:              priced,
			}
			models[aggregate.model] = summary
			order = append(order, aggregate.model)
		}
		summary.Calls += aggregate.calls
		summary.PromptTokens += aggregate.promptTokens
		summary.CachedTokens += aggregate.cachedTokens
		summary.CompletionTokens += aggregate.completionTokens
		summary.EstimatedCostMicros += cost
		result.TotalBilledCostMicros += cost
		if aggregate.cachedTokens > 0 {
			result.CachedTokensAvailable = true
		}
		earliest = earliestTime(earliest, aggregate.earliest)
		latest = laterTime(latest, aggregate.latest)
	}
	for _, model := range order {
		result.Models = append(result.Models, *models[model])
	}
	result.WindowDays = observedWindowDays(result.Since, result.Until, earliest, latest)
	if result.WindowDays > 0 {
		perDay := float64(result.TotalBilledCostMicros) / result.WindowDays
		result.ProjectedMonthlyMicros = int64(math.Round(perDay * daysPerMonth))
	}
}

// observedWindowDays returns the span the projection annualizes from, preferring
// the explicit filter bounds and falling back to the observed call range, with a
// one-day floor so a single day of data does not divide by less than one.
func observedWindowDays(since, until, earliest, latest time.Time) float64 {
	start := since
	end := until
	if start.IsZero() {
		start = earliest
	}
	if end.IsZero() {
		end = latest
	}
	if start.IsZero() || end.IsZero() {
		return 1
	}
	span := end.Sub(start).Hours() / 24.0
	if span < 1 {
		return 1
	}
	return span
}

func earliestTime(current, candidate time.Time) time.Time {
	if candidate.IsZero() {
		return current
	}
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}

func laterTime(current, candidate time.Time) time.Time {
	if candidate.After(current) {
		return candidate
	}
	return current
}
