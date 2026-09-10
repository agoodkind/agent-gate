package intake

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

// QueryFilter narrows durable intake records returned by [Query].
type QueryFilter struct {
	BucketID          string
	Offset            int
	Since             time.Time
	Until             time.Time
	System            string
	SessionID         string
	EventName         string
	ToolName          string
	DeferredState     string
	EventID           string
	Limit             int
	IncludeNormalized bool
	IncludeEnv        bool
}

// QueryResult is the complete read-only intake query response.
type QueryResult struct {
	Records []QueryRecord
	Source  string
	Note    string
}

// QueryRecord is the public query projection for a durable intake record.
// It intentionally omits raw payload bytes.
type QueryRecord struct {
	BucketID       string                        `json:"bucket_id"`
	EventID        string                        `json:"event_id"`
	RecordedAt     string                        `json:"recorded_at"`
	System         string                        `json:"system"`
	SessionID      string                        `json:"session_id"`
	TurnID         string                        `json:"turn_id,omitempty"`
	EventName      string                        `json:"event_name"`
	ToolName       string                        `json:"tool_name,omitempty"`
	ToolUseID      string                        `json:"tool_use_id,omitempty"`
	Operation      Operation                     `json:"operation"`
	RawPayloadHash string                        `json:"raw_payload_hash"`
	Classification json.RawMessage               `json:"classification,omitempty"`
	Deferred       QueryDeferred                 `json:"deferred"`
	Detail         auditstorage.DetailProjection `json:"detail"`
	NormalizedJSON json.RawMessage               `json:"normalized_json,omitempty"`
	EnvFingerprint map[string]string             `json:"env_fingerprint,omitempty"`
}

// QueryDeferred is the read projection of deferred replay state.
type QueryDeferred struct {
	State        DeferredState `json:"state"`
	PendingAt    string        `json:"pending_at,omitempty"`
	CompletedAt  string        `json:"completed_at,omitempty"`
	LastReplayAt string        `json:"last_replay_at,omitempty"`
	ReplayCount  int           `json:"replay_count"`
}

type queryArgument struct {
	Value string
}

// Query returns a globally ordered page from retained intake buckets.
func Query(ctx context.Context, cfg *config.Config, filter QueryFilter) (QueryResult, error) {
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	result := QueryResult{Source: "sqlite", Records: make([]QueryRecord, 0), Note: ""}
	err := Walk(ctx, cfg, filter, func(record QueryRecord) error { result.Records = append(result.Records, record); return nil })
	if len(result.Records) == 0 {
		result.Note = "no durable seen-event history in range"
	}
	return result, err
}

// Walk streams retained intake events; zero limit visits every matching event.
// Summary pages fix selection and order; detail reflects state at fetch time.
func Walk(ctx context.Context, cfg *config.Config, filter QueryFilter, yield func(QueryRecord) error) error {
	if err := auditstorage.ValidatePage(filter.Limit, filter.Offset); err != nil {
		return wrapLoggedError(ctx, slog.Default(), "read retained history", err)
	}
	set, err := cfg.ReadAuditHistory(ctx, filter.BucketID)
	if err != nil {
		return wrapLoggedError(ctx, slog.Default(), "read retained history", err)
	}
	defer func() { _ = set.Close() }()
	err = auditstorage.Merge(ctx, set, filter.Limit, filter.Offset,
		func(handle *auditstorage.BucketHandle, cursor *auditstorage.QuerySummary) ([]auditstorage.QuerySummary, error) {
			where, args := intakeQueryWhere(filter)
			if cursor != nil {
				if where == "" {
					where = " where "
				} else {
					where += " and "
				}
				where += intakeKeysetSQL
				args = append(args, queryArgument{Value: cursor.Time}, queryArgument{Value: strconv.FormatInt(cursor.Sequence, 10)})
			}
			rows, err := queryIntakeRows(ctx, handle.Database, intakeSummarySQL+where+intakePageSQL, args)
			if err != nil {
				return nil, wrapLoggedError(ctx, slog.Default(), "read query rows", err)
			}
			defer func() { _ = rows.Close() }()
			var result []auditstorage.QuerySummary
			for rows.Next() {
				var row auditstorage.QuerySummary
				if err := rows.Scan(&row.ID, &row.Time, &row.Sequence); err != nil {
					return nil, wrapLoggedError(ctx, slog.Default(), "read query rows", err)
				}
				result = append(result, row)
			}
			return result, rows.Err()
		},
		func(handle *auditstorage.BucketHandle, row auditstorage.QuerySummary) error {
			var selected QueryFilter
			selected.EventID, selected.Limit = row.ID, 1
			selected.IncludeNormalized = filter.IncludeNormalized
			selected.IncludeEnv = filter.IncludeEnv
			records, err := queryRecords(ctx, handle.Database, selected)
			if err != nil {
				return wrapLoggedError(ctx, slog.Default(), "read retained history", err)
			}
			if len(records) != 1 {
				return fmt.Errorf("intake event %q disappeared during read", row.ID)
			}
			records[0].BucketID = handle.Bucket.ID
			return yield(records[0])
		})
	if err != nil {
		return wrapLoggedError(ctx, slog.Default(), "walk retained history", err)
	}
	return nil
}

func queryRecords(
	ctx context.Context,
	db *sql.DB,
	filter QueryFilter,
) ([]QueryRecord, error) {
	where, args := intakeQueryWhere(filter)
	limit := ""
	if filter.Limit > 0 {
		limit = " limit " + strconv.Itoa(filter.Limit)
	}
	query := intakeQuerySelect(filter)
	allArgs := make([]queryArgument, 0, len(args)+1)
	allArgs = append(allArgs, queryArgument{Value: string(DeferredStateNone)})
	allArgs = append(allArgs, args...)
	rows, err := queryIntakeRows(ctx, db, query+where+" order by e.recorded_at desc, e.seq desc"+limit, allArgs)
	if err != nil {
		return nil, wrapLoggedError(ctx, slog.Default(), "read query rows", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	records := make([]QueryRecord, 0)
	for rows.Next() {
		record, err := scanQueryRecord(ctx, rows, filter)
		if err != nil {
			return nil, wrapLoggedError(ctx, slog.Default(), "read query rows", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapLoggedError(ctx, slog.Default(), "iterate intake query rows", err)
	}
	return records, nil
}

func intakeQuerySelect(filter QueryFilter) string {
	query := strings.ReplaceAll(querySQL2, "{{normalized}}", strconv.FormatBool(filter.IncludeNormalized))
	return strings.ReplaceAll(query, "{{environment}}", strconv.FormatBool(filter.IncludeEnv))
}

func intakeQueryWhere(filter QueryFilter) (string, []queryArgument) {
	var clauses []string
	var args []queryArgument
	add := func(clause string, value string) {
		clauses = append(clauses, clause)
		args = append(args, queryArgument{Value: value})
	}
	if !filter.Since.IsZero() {
		add("e.recorded_at >= ?", auditstorage.FormatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("e.recorded_at <= ?", auditstorage.FormatTime(filter.Until))
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
	if filter.EventID != "" {
		add("e.event_id = ?", filter.EventID)
	}
	if filter.DeferredState != "" {
		add("coalesce(d.state, ?) = ?", string(DeferredStateNone))
		args = append(args, queryArgument{Value: filter.DeferredState})
	}
	if len(clauses) == 0 {
		return "", args
	}
	return " where " + strings.Join(clauses, " and "), args
}

func queryIntakeRows(ctx context.Context, db *sql.DB, query string, args []queryArgument) (*sql.Rows, error) {
	values := make([]any, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	rows, err := db.QueryContext(ctx, query, values...)
	if err != nil {
		return nil, wrapLoggedError(ctx, slog.Default(), "query intake rows", err)
	}
	return rows, nil
}

func scanQueryRecord(
	ctx context.Context,
	rows *sql.Rows,
	filter QueryFilter,
) (QueryRecord, error) {
	var (
		record           QueryRecord
		normalized       sql.NullString
		classification   sql.NullString
		envFingerprint   sql.NullString
		recordedClasses  auditstorage.DetailMask
		availableClasses auditstorage.DetailMask
		state            string
		pendingAt        sql.NullString
		completedAt      sql.NullString
		lastReplayAt     sql.NullString
	)
	err := rows.Scan(
		&record.EventID,
		&record.RecordedAt,
		&record.System,
		&record.SessionID,
		&record.TurnID,
		&record.EventName,
		&record.ToolName,
		&record.ToolUseID,
		&record.Operation.CWD,
		&record.Operation.EffectiveCWD,
		&record.Operation.Command,
		&record.Operation.FilePath,
		&record.RawPayloadHash,
		&normalized,
		&classification,
		&envFingerprint,
		&recordedClasses,
		&availableClasses,
		&state,
		&pendingAt,
		&completedAt,
		&lastReplayAt,
		&record.Deferred.ReplayCount,
	)
	if err != nil {
		return QueryRecord{}, wrapLoggedError(ctx, slog.Default(), "scan intake query row", err)
	}
	record.Deferred.State = DeferredState(state)
	record.Deferred.PendingAt = nullStringValue(pendingAt)
	record.Deferred.CompletedAt = nullStringValue(completedAt)
	record.Deferred.LastReplayAt = nullStringValue(lastReplayAt)
	requested := auditstorage.DetailProviderEvidence
	if filter.IncludeNormalized {
		requested |= auditstorage.DetailNormalizedInput
	}
	if filter.IncludeEnv {
		requested |= auditstorage.DetailEnvironmentEvidence
	}
	record.Detail = auditstorage.ProjectDetail(recordedClasses, availableClasses, requested)
	if classification.Valid && classification.String != "" {
		record.Classification = json.RawMessage(classification.String)
	}
	if filter.IncludeNormalized && normalized.Valid && normalized.String != "" {
		record.NormalizedJSON = json.RawMessage(normalized.String)
	}
	if filter.IncludeEnv && envFingerprint.Valid && envFingerprint.String != "" {
		env, err := unmarshalEnvFingerprint(envFingerprint.String)
		if err != nil {
			return QueryRecord{}, err
		}
		record.EnvFingerprint = env
	}
	return record, nil
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

//go:embed query_2.sql
var querySQL2 string

//go:embed query_summary.sql
var intakeSummarySQL string

//go:embed query_page.sql
var intakePageSQL string

//go:embed query_keyset.sql
var intakeKeysetSQL string
