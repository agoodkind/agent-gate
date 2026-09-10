package evaluation

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
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
	BucketID           string
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
	BucketID           string                        `json:"bucket_id"`
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
	return listQueryRecords(ctx, s.database, filter)
}

// Query returns one globally ordered retained-history page.
func Query(ctx context.Context, cfg *config.Config, filter QueryFilter) (QueryResult, error) {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return QueryResult{}, wrapError("query retained evaluations", err)
	}
	if filter.ReceiptID > 0 && filter.BucketID == "" {
		return QueryResult{}, errors.New("receipt id requires bucket id")
	}
	set, err := cfg.ReadAuditHistory(ctx, filter.BucketID)
	if err != nil {
		return QueryResult{}, wrapError("query retained evaluations", err)
	}
	defer func() { _ = set.Close() }()
	result := QueryResult{Source: "sqlite", Records: make([]QueryRecord, 0), Note: "", Completeness: DetailCompleteness{IncompleteCount: 0, EarliestCompleteDetailAt: nil}}
	for _, handle := range set.Handles {
		completeness, err := queryDetailCompleteness(ctx, handle.Database, normalized)
		if err != nil {
			return QueryResult{}, wrapError("query retained evaluations", err)
		}
		result.Completeness.IncompleteCount += completeness.IncompleteCount
		candidate := completeness.EarliestCompleteDetailAt
		current := result.Completeness.EarliestCompleteDetailAt
		if candidate != nil && (current == nil || candidate.Before(*current)) {
			result.Completeness.EarliestCompleteDetailAt = candidate
		}
	}
	err = walkReadSet(ctx, set, normalized, func(record QueryRecord) error {
		result.Records = append(result.Records, record)
		return nil
	})
	if len(set.Handles) == 0 {
		result.Note = "no evaluation history exists yet"
	}
	return result, err
}

// Walk streams retained evaluations; a zero limit visits all matching records.
func Walk(ctx context.Context, cfg *config.Config, filter QueryFilter, yield func(QueryRecord) error) error {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return err
	}
	normalized.Limit = filter.Limit
	if filter.ReceiptID > 0 && filter.BucketID == "" {
		return errors.New("receipt id requires bucket id")
	}
	set, err := cfg.ReadAuditHistory(ctx, filter.BucketID)
	if err != nil {
		return wrapError("open retained evaluations", err)
	}
	defer func() { _ = set.Close() }()
	return walkReadSet(ctx, set, normalized, yield)
}

func walkReadSet(ctx context.Context, set *auditstorage.ReadSet, filter QueryFilter, yield func(QueryRecord) error) error {
	err := auditstorage.Merge(ctx, set, filter.Limit, filter.Offset,
		func(handle *auditstorage.BucketHandle, cursor *auditstorage.QuerySummary) ([]auditstorage.QuerySummary, error) {
			where, arguments := evaluationRecordWhere(filter)
			if cursor != nil {
				if where == "" {
					where = " where "
				} else {
					where += " and "
				}
				where += evaluationKeysetSQL
				arguments = append(arguments, queryArgument{Value: cursor.Time}, queryArgument{Value: cursor.ID})
			}
			rows, err := queryEvaluationRows(ctx, handle.Database, evaluationSummarySQL+where+evaluationPageSQL, arguments)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			var records []auditstorage.QuerySummary
			for rows.Next() {
				var record auditstorage.QuerySummary
				if err := rows.Scan(&record.ID, &record.Time); err != nil {
					return nil, wrapError("scan evaluation summary", err)
				}
				records = append(records, record)
			}
			if err := rows.Err(); err != nil {
				return nil, wrapError("read evaluation summaries", err)
			}
			return records, nil
		},
		func(handle *auditstorage.BucketHandle, row auditstorage.QuerySummary) error {
			selected := filter
			selected.EvaluationID, selected.Limit, selected.Offset = row.ID, 1, 0
			records, err := listQueryRecords(ctx, handle.Database, selected)
			if err != nil {
				return err
			}
			if len(records) != 1 {
				return fmt.Errorf("evaluation %q disappeared during read", row.ID)
			}
			records[0].BucketID = handle.Bucket.ID
			return yield(records[0])
		})
	if err != nil {
		return wrapError("walk retained evaluations", err)
	}
	return nil
}

//go:embed query_summary.sql
var evaluationSummarySQL string

//go:embed query_page.sql
var evaluationPageSQL string

//go:embed query_keyset.sql
var evaluationKeysetSQL string

func listQueryRecords(
	ctx context.Context,
	database *sql.DB,
	filter QueryFilter,
) ([]QueryRecord, error) {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return nil, err
	}
	where, arguments := evaluationRecordWhere(normalized)
	arguments = append(
		arguments,
		queryArgument{Value: strconv.Itoa(normalized.Limit)},
		queryArgument{Value: strconv.Itoa(normalized.Offset)},
	)
	rows, err := queryEvaluationRows(ctx, database, evaluationQuerySelect()+where+`
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
	return loadQueryDetails(ctx, database, records, normalized)
}

func loadQueryDetails(ctx context.Context, database *sql.DB, records []QueryRecord, normalized QueryFilter) ([]QueryRecord, error) {
	for i := range records {
		outcomeKnown := records[i].expectedLayerCount >= 0
		detailAvailable := normalized.DetailMode == QueryDetailFull &&
			records[i].Detail.State == auditstorage.DetailStateAvailable
		layers, err := querySafeLayers(
			ctx,
			database,
			records[i].EvaluationID,
			outcomeKnown,
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

func evaluationQuerySelect() string {
	return strings.ReplaceAll(querySQL2, "{{complete}}", evaluationCompleteDetailPredicate())
}

func evaluationCompleteDetailPredicate() string {
	return `g.content_recorded = 1
  and g.error_json is not null
  and not exists (select 1 from gate_evaluation_layers layer
   where layer.evaluation_id = g.evaluation_id and (
    layer.input_json is null or layer.output_json is null or
    layer.metadata_json is null or layer.error_message is null))
  and not exists (select 1 from gate_evaluation_labels label
   where label.evaluation_id = g.evaluation_id and label.rationale is null)`
}

func queryDetailCompleteness(ctx context.Context, database *sql.DB, filter QueryFilter) (DetailCompleteness, error) {
	normalized, err := normalizeQueryFilter(filter)
	if err != nil {
		return DetailCompleteness{}, err
	}
	where, arguments := evaluationQueryWhere(normalized)
	predicate := evaluationCompleteDetailPredicate()
	query := strings.ReplaceAll(querySQL3, "{{complete}}", predicate) + where
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
	addLayerQueryFilters(filter, &clauses, &arguments)
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
	clauses *[]string,
	arguments *[]queryArgument,
) {
	layerClauses := make([]string, 0)
	layerArguments := make([]queryArgument, 0)
	add := func(clause string, value string) {
		layerClauses = append(layerClauses, clause)
		layerArguments = append(layerArguments, queryArgument{Value: value})
	}
	if filter.RuleName != "" {
		ruleFilter := `(
 filtered_layer.rule_name = ?
 or exists (select 1 from json_each(filtered_layer.checked_rules_json) checked_rule
  where json_extract(checked_rule.value, '$.rule_name') = ?)
 )`
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
	var detailRecorded int
	var detailAvailable int
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
		&detailRecorded,
		&detailAvailable,
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
	var recorded, available auditstorage.DetailMask
	if detailRecorded != 0 {
		recorded = auditstorage.DetailEvaluationContent
	}
	if detailAvailable != 0 {
		available = auditstorage.DetailEvaluationContent
	}
	record.Detail = auditstorage.ProjectDetail(recorded, available, auditstorage.DetailEvaluationContent)
	return record, nil
}

func querySafeLayers(
	ctx context.Context,
	database *sql.DB,
	evaluationID string,
	outcomeKnown bool,
	detailAvailable bool,
) ([]QueryLayer, error) {
	rows, err := database.QueryContext(ctx, querySQL4, detailAvailable, detailAvailable, evaluationID)
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
	rows, err := database.QueryContext(ctx, querySQL5, evaluationID)
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

//go:embed query_2.sql
var querySQL2 string

//go:embed query_3.sql
var querySQL3 string

//go:embed query_4.sql
var querySQL4 string

//go:embed query_5.sql
var querySQL5 string
