package intake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

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
	EventID        string            `json:"event_id"`
	RecordedAt     string            `json:"recorded_at"`
	System         string            `json:"system"`
	SessionID      string            `json:"session_id"`
	TurnID         string            `json:"turn_id,omitempty"`
	EventName      string            `json:"event_name"`
	ToolName       string            `json:"tool_name,omitempty"`
	ToolUseID      string            `json:"tool_use_id,omitempty"`
	Operation      Operation         `json:"operation"`
	RawPayloadHash string            `json:"raw_payload_hash"`
	Deferred       QueryDeferred     `json:"deferred"`
	NormalizedJSON json.RawMessage   `json:"normalized_json,omitempty"`
	EnvFingerprint map[string]string `json:"env_fingerprint,omitempty"`
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

// Query reads durable intake history without creating schema or running
// migrations. It opens SQLite in read-only mode and treats missing intake
// tables as an empty v1 seen-event history.
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

	exists, err := tableExists(ctx, db, "intake_events")
	if err != nil {
		return QueryResult{}, err
	}
	if !exists {
		result.Note = "no durable seen-event history exists yet"
		return result, nil
	}
	hasDeferredTable, err := tableExists(ctx, db, "intake_deferred")
	if err != nil {
		return QueryResult{}, err
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

	records, err := queryRecords(ctx, db, filter, hasDeferredTable)
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
	u.RawQuery = values.Encode()
	return u.String()
}

func tableExists(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		select 1
		from sqlite_master
		where type = 'table' and name = ?
	`, tableName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, wrapLoggedError(ctx, slog.Default(), "query intake table metadata", err)
	}
	return exists == 1, nil
}

func intakeStart(ctx context.Context, db *sql.DB) (time.Time, bool, error) {
	var raw sql.NullString
	if err := db.QueryRowContext(ctx, `select min(recorded_at) from intake_events`).Scan(&raw); err != nil {
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

func queryRecords(ctx context.Context, db *sql.DB, filter QueryFilter, hasDeferredTable bool) ([]QueryRecord, error) {
	where, args := intakeQueryWhere(filter, hasDeferredTable)
	limit := ""
	if filter.Limit > 0 {
		limit = " limit " + strconv.Itoa(filter.Limit)
	}
	query := intakeQuerySelect(hasDeferredTable)
	allArgs := make([]queryArgument, 0, len(args)+1)
	if hasDeferredTable {
		allArgs = append(allArgs, queryArgument{Value: string(DeferredStateNone)})
	}
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

func intakeQuerySelect(hasDeferredTable bool) string {
	if !hasDeferredTable {
		return `
		select
			e.event_id,
			e.recorded_at,
			e.system,
			e.session_id,
			e.turn_id,
			e.event_name,
			e.tool_name,
			e.tool_use_id,
			e.cwd,
			e.effective_cwd,
			e.command,
			e.file_path,
			e.raw_payload_hash,
			e.normalized_json,
			e.env_fingerprint_json,
			'none',
			null as pending_at,
			null as completed_at,
			null as last_replay_at,
			0
		from intake_events e
	`
	}
	return `
		select
			e.event_id,
			e.recorded_at,
			e.system,
			e.session_id,
			e.turn_id,
			e.event_name,
			e.tool_name,
			e.tool_use_id,
			e.cwd,
			e.effective_cwd,
			e.command,
			e.file_path,
			e.raw_payload_hash,
			e.normalized_json,
			e.env_fingerprint_json,
			coalesce(d.state, ?),
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			coalesce(d.replay_count, 0)
		from intake_events e
		left join intake_receipts r on r.receipt_id = (
			select max(receipt_id) from intake_receipts where event_id = e.event_id
		)
		left join intake_deferred d on d.receipt_id = r.receipt_id
	`
}

func intakeQueryWhere(filter QueryFilter, hasDeferredTable bool) (string, []queryArgument) {
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
		if hasDeferredTable {
			add("coalesce(d.state, ?) = ?", string(DeferredStateNone))
			args = append(args, queryArgument{Value: filter.DeferredState})
		} else if filter.DeferredState != string(DeferredStateNone) {
			clauses = append(clauses, "1 = 0")
		}
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

func scanQueryRecord(ctx context.Context, rows *sql.Rows, filter QueryFilter) (QueryRecord, error) {
	var (
		record         QueryRecord
		normalized     string
		envFingerprint string
		state          string
		pendingAt      sql.NullString
		completedAt    sql.NullString
		lastReplayAt   sql.NullString
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
		&envFingerprint,
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
	if filter.IncludeNormalized {
		record.NormalizedJSON = json.RawMessage(normalized)
	}
	if filter.IncludeEnv {
		env, err := unmarshalEnvFingerprint(envFingerprint)
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
