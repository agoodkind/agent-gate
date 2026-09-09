package intake

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

// QueryFilter narrows durable intake records returned by [Query].
type QueryFilter struct {
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

// Query reads durable intake history through a read-only connection.
func Query(ctx context.Context, cfg *config.Config, filter QueryFilter) (QueryResult, error) {
	path := config.DefaultAuditSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	result := QueryResult{
		Records: nil,
		Source:  "sqlite",
		Note:    "",
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Note = "no durable seen-event history exists yet"
			return result, nil
		}
		return QueryResult{}, wrapLoggedError(ctx, slog.Default(), "stat intake sqlite path", err)
	}
	db, err := sql.Open("sqlite3", readOnlySQLiteDSN(path))
	if err != nil {
		return QueryResult{}, wrapLoggedError(ctx, slog.Default(), "open intake sqlite db read-only", err)
	}
	defer func() {
		_ = db.Close()
	}()
	if err := db.PingContext(ctx); err != nil {
		return QueryResult{}, wrapLoggedError(ctx, slog.Default(), "ping intake sqlite db read-only", err)
	}

	start, hasRows, err := intakeStart(ctx, db)
	if err != nil {
		return QueryResult{}, err
	}
	if !hasRows {
		result.Note = "no seen events have been recorded yet"
		return result, nil
	}
	if !filter.Until.IsZero() && filter.Until.Before(start) {
		result.Note = "seen-event history starts at " + formatTime(start) + "; use query decisions for earlier audit history"
		return result, nil
	}
	if !filter.Since.IsZero() && filter.Since.Before(start) {
		filter.Since = start
		result.Note = "clamped lower bound to seen-event history start " + formatTime(start)
	}

	records, err := queryRecords(ctx, db, filter)
	if err != nil {
		return QueryResult{}, err
	}
	result.Records = records
	return result, nil
}

func readOnlySQLiteDSN(path string) string {
	u := url.URL{
		Scheme: "file",
		Path:   path,
	}
	values := url.Values{}
	values.Set("mode", "ro")
	values.Set("_foreign_keys", "on")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "5000")
	u.RawQuery = values.Encode()
	return u.String()
}

func intakeStart(ctx context.Context, db *sql.DB) (time.Time, bool, error) {
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, querySQL1).Scan(&raw); err != nil {
		return time.Time{}, false, wrapLoggedError(ctx, slog.Default(), "query intake history start", err)
	}
	if !raw.Valid || raw.String == "" {
		return time.Time{}, false, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return time.Time{}, false, wrapLoggedError(ctx, slog.Default(), "parse intake history start", err)
	}
	return parsed, true, nil
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
		return nil, err
	}
	defer func() {
		_ = rows.Close()
	}()

	records := make([]QueryRecord, 0)
	for rows.Next() {
		record, err := scanQueryRecord(ctx, rows, filter)
		if err != nil {
			return nil, err
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
		add("e.recorded_at >= ?", filter.Since.UTC().Format(time.RFC3339Nano))
	}
	if !filter.Until.IsZero() {
		add("e.recorded_at <= ?", filter.Until.UTC().Format(time.RFC3339Nano))
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

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

//go:embed query_1.sql
var querySQL1 string

//go:embed query_2.sql
var querySQL2 string
