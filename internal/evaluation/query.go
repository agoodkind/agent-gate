package evaluation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

const (
	// DefaultQueryLimit bounds evaluation queries that do not specify a limit.
	DefaultQueryLimit = 50
	// MaxQueryLimit is the largest evaluation page accepted by the query API.
	MaxQueryLimit = 1000
	// QueryDetailFull loads and validates stored evaluation content.
	QueryDetailFull = "full"
	// QueryDetailSummary returns summary rows without loading evaluation content.
	QueryDetailSummary = "summary"
)

// QueryFilter narrows evaluation records and their joined intake metadata.
type QueryFilter struct {
	EvaluationID       string
	EventID            string
	ReceiptID          int64
	Mode               string
	Since              time.Time
	Until              time.Time
	System             string
	SessionID          string
	EventName          string
	ToolName           string
	RuleName           string
	LayerName          string
	LayerKind          string
	LayerOutcome       string
	ModelName          string
	FinalVerdict       string
	DetailMode         string
	CompleteDetailOnly bool
	Limit              int
	Offset             int
}

// DetailCompleteness reports missing evaluation detail across a filtered selection.
type DetailCompleteness struct {
	IncompleteCount          int
	EarliestCompleteDetailAt *time.Time
}

// QueryResult is a read-only page plus completeness for the full filtered selection.
type QueryResult struct {
	Records      []QueryRecord
	Source       string
	Note         string
	Completeness DetailCompleteness
}

// QueryRecord contains safe evaluation and intake metadata plus ordered children.
type QueryRecord struct {
	EvaluationID       string                        `json:"evaluation_id"`
	ReceiptID          int64                         `json:"receipt_id"`
	EventID            string                        `json:"event_id"`
	Attempt            int                           `json:"attempt"`
	Mode               string                        `json:"mode"`
	System             string                        `json:"system"`
	SessionID          string                        `json:"session_id"`
	EventName          string                        `json:"event_name"`
	ToolName           string                        `json:"tool_name,omitempty"`
	ConfigHash         string                        `json:"config_hash"`
	EngineVersion      string                        `json:"engine_version"`
	EngineCommit       string                        `json:"engine_commit"`
	EngineBuildHash    string                        `json:"engine_build_hash"`
	InputHash          string                        `json:"input_hash"`
	StartedAt          time.Time                     `json:"started_at"`
	CompletedAt        time.Time                     `json:"completed_at"`
	FinalVerdict       string                        `json:"final_verdict"`
	FinalSource        string                        `json:"final_source"`
	EnforcementAction  string                        `json:"enforcement_action"`
	Enforced           bool                          `json:"enforced"`
	TotalLatencyUS     int64                         `json:"total_latency_us"`
	Detail             auditstorage.DetailProjection `json:"detail"`
	Layers             []QueryLayer                  `json:"layers"`
	Labels             []QueryLabel                  `json:"labels"`
	expectedLayerCount int                           `json:"-"`
	expectedLabelCount int                           `json:"-"`
}

// QueryLayer is the safe training projection of one ordered layer.
type QueryLayer struct {
	LayerIndex        int             `json:"layer_index"`
	ParentLayerIndex  *int            `json:"parent_layer_index,omitempty"`
	Kind              string          `json:"kind"`
	Name              string          `json:"name"`
	Status            string          `json:"status"`
	Outcome           string          `json:"outcome,omitempty"`
	Verdict           string          `json:"verdict,omitempty"`
	InputReference    string          `json:"input_reference"`
	InputHash         string          `json:"input_hash"`
	OutputHash        string          `json:"output_hash"`
	Output            json.RawMessage `json:"output,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	StartedAt         time.Time       `json:"started_at"`
	CompletedAt       time.Time       `json:"completed_at"`
	LatencyUS         int64           `json:"latency_us"`
	ServiceName       string          `json:"service_name,omitempty"`
	ServiceVersion    string          `json:"service_version,omitempty"`
	ModelName         string          `json:"model_name,omitempty"`
	ModelVersion      string          `json:"model_version,omitempty"`
	PromptHash        string          `json:"prompt_hash,omitempty"`
	SchemaHash        string          `json:"schema_hash,omitempty"`
	CacheStatus       string          `json:"cache_status,omitempty"`
	CacheKeyHash      string          `json:"cache_key_hash,omitempty"`
	CacheEntryVersion *int64          `json:"cache_entry_version,omitempty"`
	CacheExpiresAt    *time.Time      `json:"cache_expires_at,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	RetryCount        int             `json:"retry_count"`
}

// QueryLabel omits free-form rationale from the training export.
type QueryLabel struct {
	Namespace    string    `json:"namespace"`
	LabelVersion int       `json:"label_version"`
	Verdict      string    `json:"verdict"`
	Source       string    `json:"source"`
	Confidence   *float64  `json:"confidence,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type queryArgument struct {
	Value string
}

// List returns a bounded page of evaluations from an initialized store.
func (s *Store) List(ctx context.Context, filter QueryFilter) ([]QueryRecord, error) {
	if s == nil || s.database == nil {
		return nil, errors.New("evaluation store is unavailable")
	}
	return listQueryRecords(ctx, s.database, filter, true, true, true, true)
}

// Query reads evaluations from an existing SQLite path without creating or migrating it.
func Query(ctx context.Context, path string, filter QueryFilter) (QueryResult, error) {
	result := QueryResult{
		Records: make([]QueryRecord, 0),
		Source:  "sqlite",
		Note:    "",
		Completeness: DetailCompleteness{
			IncompleteCount: 0, EarliestCompleteDetailAt: nil,
		},
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Note = "no evaluation history exists yet"
			return result, nil
		}
		return QueryResult{}, wrapError("stat evaluation sqlite path", err)
	}
	database, err := sql.Open("sqlite3", queryReadOnlySQLiteDSN(path))
	if err != nil {
		return QueryResult{}, wrapError("open evaluation sqlite db read-only", err)
	}
	defer func() {
		_ = database.Close()
	}()
	if err := database.PingContext(ctx); err != nil {
		return QueryResult{}, wrapError("ping evaluation sqlite db read-only", err)
	}
	exists, err := queryTableExists(ctx, database, "gate_evaluations")
	if err != nil {
		return QueryResult{}, err
	}
	if !exists {
		result.Note = "no evaluation history exists yet"
		return result, nil
	}
	var count int
	if err := database.QueryRowContext(ctx, `select count(*) from gate_evaluations`).Scan(&count); err != nil {
		return QueryResult{}, wrapError("count evaluation rows", err)
	}
	if count == 0 {
		result.Note = "no evaluations have been recorded yet"
		return result, nil
	}
	hasOutcome, err := queryLayerOutcomeColumnExists(ctx, database)
	if err != nil {
		return QueryResult{}, err
	}
	hasChildCounts, err := queryChildCountColumnsExist(ctx, database)
	if err != nil {
		return QueryResult{}, err
	}
	hasVerdict, err := queryLayerVerdictColumnExists(ctx, database)
	if err != nil {
		return QueryResult{}, err
	}
	hasSplitDetail, err := queryTableExists(ctx, database, "gate_evaluation_layer_details")
	if err != nil {
		return QueryResult{}, err
	}
	records, err := listQueryRecords(
		ctx, database, filter, hasOutcome, hasChildCounts, hasVerdict, hasSplitDetail,
	)
	if err != nil {
		return QueryResult{}, err
	}
	completeness, err := queryDetailCompleteness(ctx, database, filter, hasOutcome, hasSplitDetail)
	if err != nil {
		return QueryResult{}, err
	}
	result.Records = records
	result.Completeness = completeness
	return result, nil
}

func listQueryRecords(
	ctx context.Context,
	database *sql.DB,
	filter QueryFilter,
	hasOutcome bool,
	hasChildCounts bool,
	hasVerdict bool,
	hasSplitDetail bool,
) ([]QueryRecord, error) {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return nil, err
	}
	where, arguments := evaluationRecordWhere(normalized, hasOutcome, hasSplitDetail)
	arguments = append(
		arguments,
		queryArgument{Value: strconv.Itoa(normalized.Limit)},
		queryArgument{Value: strconv.Itoa(normalized.Offset)},
	)
	rows, err := queryEvaluationRows(ctx, database, evaluationQuerySelect(hasChildCounts, hasSplitDetail)+where+`
		order by g.completed_at desc, g.evaluation_id desc
		limit ? offset ?
	`, arguments)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()
	records := make([]QueryRecord, 0)
	for rows.Next() {
		record, err := scanQueryEvaluation(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, wrapError("iterate evaluation rows", err)
	}
	if err := rows.Close(); err != nil {
		return nil, wrapError("close evaluation rows", err)
	}
	for i := range records {
		outcomeKnown := hasOutcome && records[i].expectedLayerCount >= 0
		detailAvailable := normalized.DetailMode == QueryDetailFull &&
			(records[i].Detail.State == auditstorage.DetailStateAvailable ||
				records[i].Detail.State == auditstorage.DetailStateProtected)
		layers, err := querySafeLayers(
			ctx,
			database,
			records[i].EvaluationID,
			hasOutcome,
			outcomeKnown,
			hasVerdict,
			hasSplitDetail,
			detailAvailable,
		)
		if err != nil {
			return nil, err
		}
		if records[i].expectedLayerCount >= 0 &&
			len(layers) != records[i].expectedLayerCount {
			message := fmt.Sprintf(
				"evaluation %q has %d layers; expected %d",
				records[i].EvaluationID,
				len(layers),
				records[i].expectedLayerCount,
			)
			return nil, errors.New(message)
		}
		if records[i].expectedLayerCount < 0 && len(layers) == 0 {
			return nil, fmt.Errorf("evaluation %q has no layers", records[i].EvaluationID)
		}
		labels, err := querySafeLabels(ctx, database, records[i].EvaluationID)
		if err != nil {
			return nil, err
		}
		if records[i].expectedLabelCount >= 0 &&
			len(labels) != records[i].expectedLabelCount {
			message := fmt.Sprintf(
				"evaluation %q has %d labels; expected %d",
				records[i].EvaluationID,
				len(labels),
				records[i].expectedLabelCount,
			)
			return nil, errors.New(message)
		}
		records[i].Layers = layers
		records[i].Labels = labels
	}
	return records, nil
}

func evaluationQuerySelect(hasChildCounts bool, hasSplitDetail bool) string {
	childCounts := "-1, -1"
	if hasChildCounts {
		childCounts = "g.layer_count, g.label_count"
	}
	detailColumns := `'available', 1, 1, 0`
	if hasSplitDetail {
		detailColumns = `g.detail_state,
			case when g.detail_state != 'not_recorded' then 1 else 0 end,
			case when ` + evaluationCompleteDetailPredicate(true) + ` then 1 else 0 end,
			case when g.detail_state = 'protected' and (
				exists (select 1 from intake_deferred pending
					where pending.receipt_id = g.receipt_id and pending.state = 'pending')
				or exists (select 1 from deferred_audit_outbox pending_outbox
					where pending_outbox.receipt_id = g.receipt_id
						and pending_outbox.state = 'pending')
				or exists (select 1 from deferred_audit_outbox_entries undelivered
					where undelivered.receipt_id = g.receipt_id
						and undelivered.delivered_at is null)
			) then 1 else 0 end`
	}
	return `select g.evaluation_id, g.receipt_id, g.event_id, g.attempt, g.mode,
		e.system, e.session_id, e.event_name, e.tool_name,
		g.config_hash, g.engine_version, g.engine_commit, g.engine_build_hash,
		g.input_hash, g.started_at, g.completed_at, g.final_verdict,
		g.final_source, g.enforcement_action, g.enforced, g.total_latency_us, ` + childCounts + `,
		` + detailColumns + `
	from gate_evaluations g
	join intake_events e on e.event_id = g.event_id
`
}

func evaluationCompleteDetailPredicate(hasSplitDetail bool) string {
	if !hasSplitDetail {
		return "1 = 1"
	}
	return `g.detail_state in ('available', 'protected')
		and exists (
			select 1 from gate_evaluation_details evaluation_detail
			where evaluation_detail.evaluation_id = g.evaluation_id
		)
		and (select count(*) from gate_evaluation_layer_details layer_detail
			where layer_detail.evaluation_id = g.evaluation_id) =
			(select count(*) from gate_evaluation_layers layer_summary
				where layer_summary.evaluation_id = g.evaluation_id)
		and (select count(*) from gate_evaluation_label_details label_detail
			where label_detail.evaluation_id = g.evaluation_id) =
			(select count(*) from gate_evaluation_labels label_summary
				where label_summary.evaluation_id = g.evaluation_id)`
}

func queryDetailCompleteness(ctx context.Context, database *sql.DB, filter QueryFilter, hasOutcome bool, hasSplitDetail bool) (DetailCompleteness, error) {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return DetailCompleteness{}, err
	}
	where, arguments := evaluationQueryWhere(normalized, hasOutcome, hasSplitDetail)
	predicate := evaluationCompleteDetailPredicate(hasSplitDetail)
	query := `select
		count(*) - coalesce(sum(case when ` + predicate + ` then 1 else 0 end), 0),
		min(case when ` + predicate + ` then g.completed_at end)
	from gate_evaluations g
	join intake_events e on e.event_id = g.event_id` + where
	values := make([]any, 0, len(arguments))
	for _, argument := range arguments {
		values = append(values, argument.Value)
	}
	var completeness DetailCompleteness
	var earliestCompleteDetailAt sql.NullString
	if err := database.QueryRowContext(ctx, query, values...).Scan(&completeness.IncompleteCount, &earliestCompleteDetailAt); err != nil {
		return DetailCompleteness{}, wrapError("query evaluation detail completeness", err)
	}
	if earliestCompleteDetailAt.Valid {
		parsed, err := parseTime(earliestCompleteDetailAt.String)
		if err != nil {
			return DetailCompleteness{}, err
		}
		completeness.EarliestCompleteDetailAt = &parsed
	}
	return completeness, nil
}

func normalizeQueryFilter(filter QueryFilter) (QueryFilter, error) {
	if filter.Limit < 0 || filter.Limit > MaxQueryLimit {
		return QueryFilter{}, fmt.Errorf("evaluation query limit must be between 0 and %d", MaxQueryLimit)
	}
	if filter.Offset < 0 {
		return QueryFilter{}, errors.New("evaluation query offset must not be negative")
	}
	if filter.Limit == 0 {
		filter.Limit = DefaultQueryLimit
	}
	if filter.DetailMode == "" {
		filter.DetailMode = QueryDetailFull
	}
	if filter.DetailMode != QueryDetailFull && filter.DetailMode != QueryDetailSummary {
		return QueryFilter{}, errors.New("evaluation query detail mode must be full or summary")
	}
	return filter, nil
}

func evaluationQueryWhere(
	filter QueryFilter,
	hasOutcome bool,
	hasSplitDetail bool,
) (string, []queryArgument) {
	clauses := make([]string, 0)
	arguments := make([]queryArgument, 0)
	add := func(clause string, value string) {
		clauses = append(clauses, clause)
		arguments = append(arguments, queryArgument{Value: value})
	}
	if filter.EvaluationID != "" {
		add("g.evaluation_id = ?", filter.EvaluationID)
	}
	if filter.EventID != "" {
		add("g.event_id = ?", filter.EventID)
	}
	if filter.ReceiptID != 0 {
		add("g.receipt_id = ?", strconv.FormatInt(filter.ReceiptID, 10))
	}
	if filter.Mode != "" {
		add("g.mode = ?", filter.Mode)
	}
	if !filter.Since.IsZero() {
		add("g.completed_at >= ?", formatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("g.completed_at <= ?", formatTime(filter.Until))
	}
	if filter.System != "" {
		add("e.system = ?", filter.System)
	}
	if filter.SessionID != "" {
		add("e.session_id = ?", filter.SessionID)
	}
	if filter.EventName != "" {
		add("e.event_name = ?", filter.EventName)
	}
	if filter.ToolName != "" {
		add("e.tool_name = ?", filter.ToolName)
	}
	addLayerQueryFilters(filter, hasOutcome, hasSplitDetail, &clauses, &arguments)
	if filter.FinalVerdict != "" {
		add("g.final_verdict = ?", filter.FinalVerdict)
	}
	if len(clauses) == 0 {
		return "", arguments
	}
	return " where " + strings.Join(clauses, " and "), arguments
}

func addLayerQueryFilters(
	filter QueryFilter,
	hasOutcome bool,
	hasSplitDetail bool,
	clauses *[]string,
	arguments *[]queryArgument,
) {
	if filter.LayerOutcome != "" && !hasOutcome {
		*clauses = append(*clauses, "1 = 0")
		return
	}
	layerClauses := make([]string, 0)
	layerArguments := make([]queryArgument, 0)
	add := func(clause string, value string) {
		layerClauses = append(layerClauses, clause)
		layerArguments = append(layerArguments, queryArgument{Value: value})
	}
	if filter.RuleName != "" {
		ruleFilter := `(
			json_extract(filtered_layer.metadata_json, '$.rule_name') = ?
			or exists (
				select 1
				from json_each(filtered_layer.metadata_json, '$.checked_rules') checked_rule
				where json_extract(checked_rule.value, '$.rule_name') = ?
			)
		)`
		if hasSplitDetail {
			ruleFilter = `(
				filtered_layer.rule_name = ?
				or exists (
					select 1
					from json_each(filtered_layer.checked_rules_json) checked_rule
					where json_extract(checked_rule.value, '$.rule_name') = ?
				)
			)`
		}
		layerClauses = append(layerClauses, ruleFilter)
		layerArguments = append(
			layerArguments,
			queryArgument{Value: filter.RuleName},
			queryArgument{Value: filter.RuleName},
		)
	}
	if filter.LayerName != "" {
		add("filtered_layer.name = ?", filter.LayerName)
	}
	if filter.LayerKind != "" {
		add("filtered_layer.kind = ?", filter.LayerKind)
	}
	if filter.LayerOutcome != "" {
		add("filtered_layer.outcome = ?", filter.LayerOutcome)
	}
	if filter.ModelName != "" {
		add("filtered_layer.model_name = ?", filter.ModelName)
	}
	if len(layerClauses) == 0 {
		return
	}
	*arguments = append(*arguments, layerArguments...)
	*clauses = append(*clauses, `exists (
		select 1
		from gate_evaluation_layers filtered_layer
		where filtered_layer.evaluation_id = g.evaluation_id
		and `+strings.Join(layerClauses, " and ")+`
	)`)
}

func queryEvaluationRows(
	ctx context.Context,
	database *sql.DB,
	query string,
	arguments []queryArgument,
) (*sql.Rows, error) {
	values := make([]any, 0, len(arguments))
	for _, argument := range arguments {
		values = append(values, argument.Value)
	}
	rows, err := database.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, wrapError("query evaluation rows", err)
	}
	return rows, nil
}

func scanQueryEvaluation(rows *sql.Rows) (QueryRecord, error) {
	var record QueryRecord
	var startedAt string
	var completedAt string
	var detailState auditstorage.DetailState
	var detailRecorded int
	var detailAvailable int
	var detailProtected int
	if err := rows.Scan(
		&record.EvaluationID,
		&record.ReceiptID,
		&record.EventID,
		&record.Attempt,
		&record.Mode,
		&record.System,
		&record.SessionID,
		&record.EventName,
		&record.ToolName,
		&record.ConfigHash,
		&record.EngineVersion,
		&record.EngineCommit,
		&record.EngineBuildHash,
		&record.InputHash,
		&startedAt,
		&completedAt,
		&record.FinalVerdict,
		&record.FinalSource,
		&record.EnforcementAction,
		&record.Enforced,
		&record.TotalLatencyUS,
		&record.expectedLayerCount,
		&record.expectedLabelCount,
		&detailState,
		&detailRecorded,
		&detailAvailable,
		&detailProtected,
	); err != nil {
		return QueryRecord{}, wrapError("scan evaluation row", err)
	}
	var err error
	record.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return QueryRecord{}, err
	}
	record.CompletedAt, err = parseTime(completedAt)
	if err != nil {
		return QueryRecord{}, err
	}
	recordedClasses := make([]auditstorage.DetailClass, 0, 1)
	if detailRecorded != 0 {
		recordedClasses = append(recordedClasses, auditstorage.DetailClassEvaluationContent)
	}
	availableClasses := make([]auditstorage.DetailClass, 0, 1)
	if detailAvailable != 0 {
		availableClasses = append(availableClasses, auditstorage.DetailClassEvaluationContent)
	}
	protectedClasses := make([]auditstorage.DetailClass, 0, 1)
	if detailProtected != 0 {
		protectedClasses = append(protectedClasses, auditstorage.DetailClassEvaluationContent)
	}
	record.Detail = auditstorage.ProjectDetail(
		recordedClasses,
		availableClasses,
		[]auditstorage.DetailClass{auditstorage.DetailClassEvaluationContent},
		detailState,
		"",
		protectedClasses,
	)
	return record, nil
}

func querySafeLayers(
	ctx context.Context,
	database *sql.DB,
	evaluationID string,
	hasOutcome bool,
	outcomeKnown bool,
	hasVerdict bool,
	hasSplitDetail bool,
	detailAvailable bool,
) ([]QueryLayer, error) {
	outcomeColumn := "''"
	if hasOutcome {
		outcomeColumn = "outcome"
	}
	verdictColumn := "''"
	if hasVerdict {
		verdictColumn = "verdict"
	}
	detailColumns := "output_json, metadata_json"
	detailJoin := ""
	if hasSplitDetail {
		detailColumns = "null, null"
		if detailAvailable {
			detailColumns = "detail.output_json, detail.metadata_json"
			detailJoin = ` join gate_evaluation_layer_details detail
				on detail.evaluation_id = layer.evaluation_id
				and detail.layer_index = layer.layer_index`
		}
	}
	query := `select layer.layer_index, layer.parent_layer_index, layer.kind, layer.name, layer.status, ` + outcomeColumn + `, ` + verdictColumn + `,
		input_reference, input_hash, output_hash, output_json, metadata_json,
		started_at, completed_at, latency_us, service_name, service_version,
		model_name, model_version, prompt_hash, schema_hash, cache_status,
		cache_key_hash, cache_entry_version, cache_expires_at, error_code, retry_count
		from gate_evaluation_layers layer` + detailJoin + `
		where layer.evaluation_id = ? order by layer.layer_index`
	query = strings.Replace(query, "output_json, metadata_json", detailColumns, 1)
	rows, err := database.QueryContext(ctx, query, evaluationID)
	if err != nil {
		return nil, wrapError("query safe evaluation layers", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	layers := make([]QueryLayer, 0)
	for rows.Next() {
		layer, err := scanQueryLayer(rows)
		if err != nil {
			return nil, err
		}
		normalized, err := validateQueryLayer(
			layer, len(layers), outcomeKnown, detailAvailable,
		)
		if err != nil {
			return nil, wrapError(fmt.Sprintf("validate evaluation %q layer", evaluationID), err)
		}
		layers = append(layers, normalized)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate safe evaluation layers", err)
	}
	return layers, nil
}

func scanQueryLayer(rows *sql.Rows) (QueryLayer, error) {
	var layer QueryLayer
	var parentIndex sql.NullInt64
	var output []byte
	var metadata []byte
	var startedAt string
	var completedAt string
	var cacheVersion sql.NullInt64
	var cacheExpiry sql.NullString
	if err := rows.Scan(
		&layer.LayerIndex,
		&parentIndex,
		&layer.Kind,
		&layer.Name,
		&layer.Status,
		&layer.Outcome,
		&layer.Verdict,
		&layer.InputReference,
		&layer.InputHash,
		&layer.OutputHash,
		&output,
		&metadata,
		&startedAt,
		&completedAt,
		&layer.LatencyUS,
		&layer.ServiceName,
		&layer.ServiceVersion,
		&layer.ModelName,
		&layer.ModelVersion,
		&layer.PromptHash,
		&layer.SchemaHash,
		&layer.CacheStatus,
		&layer.CacheKeyHash,
		&cacheVersion,
		&cacheExpiry,
		&layer.ErrorCode,
		&layer.RetryCount,
	); err != nil {
		return QueryLayer{}, wrapError("scan safe evaluation layer", err)
	}
	if parentIndex.Valid {
		converted := int(parentIndex.Int64)
		layer.ParentLayerIndex = &converted
	}
	layer.Output = append(json.RawMessage(nil), output...)
	layer.Metadata = append(json.RawMessage(nil), metadata...)
	var err error
	layer.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return QueryLayer{}, err
	}
	layer.CompletedAt, err = parseTime(completedAt)
	if err != nil {
		return QueryLayer{}, err
	}
	if cacheVersion.Valid {
		value := cacheVersion.Int64
		layer.CacheEntryVersion = &value
	}
	if cacheExpiry.Valid {
		value, err := parseTime(cacheExpiry.String)
		if err != nil {
			return QueryLayer{}, err
		}
		layer.CacheExpiresAt = &value
	}
	return layer, nil
}

func validateQueryLayer(
	layer QueryLayer,
	position int,
	outcomeKnown bool,
	detailAvailable bool,
) (QueryLayer, error) {
	if layer.LayerIndex != position {
		return QueryLayer{}, fmt.Errorf(
			"layer index %d is not ordered position %d",
			layer.LayerIndex,
			position,
		)
	}
	if layer.ParentLayerIndex != nil &&
		(*layer.ParentLayerIndex < 0 || *layer.ParentLayerIndex >= position) {
		return QueryLayer{}, fmt.Errorf(
			"layer index %d has invalid parent %d",
			layer.LayerIndex,
			*layer.ParentLayerIndex,
		)
	}
	if err := validateReadLayerSemantics(
		layer.Kind,
		layer.Status,
		layer.Outcome,
		outcomeKnown,
	); err != nil {
		return QueryLayer{}, fmt.Errorf(
			"layer index %d has invalid semantics: %s",
			layer.LayerIndex,
			err.Error(),
		)
	}
	if !detailAvailable {
		return layer, nil
	}
	if err := unmarshalJSONObject(layer.Output, "output"); err != nil {
		return QueryLayer{}, fmt.Errorf("layer index %d: %s", layer.LayerIndex, err.Error())
	}
	if err := validateLayerOutputHash(layer.Output, layer.OutputHash); err != nil {
		return QueryLayer{}, fmt.Errorf(
			"layer index %d has invalid output: %s",
			layer.LayerIndex,
			err.Error(),
		)
	}
	normalizedMetadata, err := UnmarshalLayerMetadata(layer.Metadata)
	if err != nil {
		return QueryLayer{}, fmt.Errorf("layer index %d: %s", layer.LayerIndex, err.Error())
	}
	layer.Metadata = normalizedMetadata
	return layer, nil
}

func unmarshalJSONObject(raw json.RawMessage, name string) error {
	var value map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value == nil {
		return fmt.Errorf("%s is not a JSON object", name)
	}
	return nil
}

func querySafeLabels(
	ctx context.Context,
	database *sql.DB,
	evaluationID string,
) ([]QueryLabel, error) {
	rows, err := database.QueryContext(ctx, `
		select namespace, label_version, verdict, source, confidence, created_at
		from gate_evaluation_labels
		where evaluation_id = ?
		order by namespace, label_version
	`, evaluationID)
	if err != nil {
		return nil, wrapError("query safe evaluation labels", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	labels := make([]QueryLabel, 0)
	for rows.Next() {
		var label QueryLabel
		var confidence sql.NullFloat64
		var createdAt string
		if err := rows.Scan(
			&label.Namespace,
			&label.LabelVersion,
			&label.Verdict,
			&label.Source,
			&confidence,
			&createdAt,
		); err != nil {
			return nil, wrapError("scan safe evaluation label", err)
		}
		if confidence.Valid {
			value := confidence.Float64
			label.Confidence = &value
		}
		label.CreatedAt, err = parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		labels = append(labels, label)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate safe evaluation labels", err)
	}
	return labels, nil
}

func queryTableExists(ctx context.Context, database *sql.DB, tableName string) (bool, error) {
	var exists int
	err := database.QueryRowContext(ctx, `
		select 1 from sqlite_master where type = 'table' and name = ?
	`, tableName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, wrapError("query evaluation table metadata", err)
	}
	return exists == 1, nil
}

func queryLayerOutcomeColumnExists(ctx context.Context, database *sql.DB) (bool, error) {
	rows, err := database.QueryContext(ctx, `pragma table_info(gate_evaluation_layers)`)
	if err != nil {
		return false, wrapError("query evaluation column metadata", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, wrapError("scan evaluation column metadata", err)
		}
		if name == "outcome" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapError("iterate evaluation column metadata", err)
	}
	return false, nil
}

func queryLayerVerdictColumnExists(ctx context.Context, database *sql.DB) (bool, error) {
	rows, err := database.QueryContext(ctx, `pragma table_info(gate_evaluation_layers)`)
	if err != nil {
		return false, wrapError("query evaluation verdict column metadata", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, wrapError("scan evaluation verdict column metadata", err)
		}
		if name == "verdict" {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapError("iterate evaluation verdict column metadata", err)
	}
	return false, nil
}

func queryChildCountColumnsExist(ctx context.Context, database *sql.DB) (bool, error) {
	rows, err := database.QueryContext(ctx, `pragma table_info(gate_evaluations)`)
	if err != nil {
		return false, wrapError("query evaluation child count metadata", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	foundLayerCount := false
	foundLabelCount := false
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultValue,
			&primaryKey,
		); err != nil {
			return false, wrapError("scan evaluation child count metadata", err)
		}
		if name == "layer_count" {
			foundLayerCount = true
		}
		if name == "label_count" {
			foundLabelCount = true
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapError("iterate evaluation child count metadata", err)
	}
	return foundLayerCount && foundLabelCount, nil
}

func queryReadOnlySQLiteDSN(path string) string {
	value := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	value.RawQuery = query.Encode()
	return value.String()
}
