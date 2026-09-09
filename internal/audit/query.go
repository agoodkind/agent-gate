package audit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

// QueryRecord is the public query projection of an audit event.
type QueryRecord struct {
	Event
	Detail auditstorage.DetailProjection `json:"detail"`
}

// QueryFilter narrows the set of audit events returned by [Query].
type QueryFilter struct {
	Since     time.Time
	Until     time.Time
	System    string
	SessionID string
	EventName string
	ToolName  string
	Decision  string
	Rule      string
	Limit     int
}

type queryArg struct {
	Value string
}

// Query returns audit events matching filter from the SQLite audit store. The
// returned source name is always "sqlite"; it is retained for callers that
// surface which backend served the query.
func Query(cfg *config.Config, filter QueryFilter) ([]QueryRecord, string, error) {
	events, err := querySQLiteContext(context.Background(), cfg, filter)
	if err != nil {
		return nil, "sqlite", err
	}
	return events, "sqlite", nil
}

// QueryReadOnly returns audit events through a read-only SQLite connection.
func QueryReadOnly(
	ctx context.Context,
	cfg *config.Config,
	filter QueryFilter,
) ([]QueryRecord, string, error) {
	events, err := querySQLiteContext(ctx, cfg, filter)
	if err != nil {
		return nil, "sqlite", err
	}
	return events, "sqlite", nil
}

func querySQLiteContext(
	ctx context.Context,
	cfg *config.Config,
	filter QueryFilter,
) ([]QueryRecord, error) {
	log := slog.Default()
	path := config.DefaultAuditSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	if _, err := os.Stat(path); err != nil {
		log.WarnContext(ctx, "stat audit sqlite path failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("stat audit sqlite path: %w", err)
	}
	databasePath := auditReadOnlySQLiteDSN(path)
	db, err := sql.Open("sqlite3", databasePath)
	if err != nil {
		log.WarnContext(ctx, "open audit sqlite db failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("open audit sqlite db: %w", err)
	}
	defer func() { _ = db.Close() }()

	where, args := queryWhere(filter)
	limit := ""
	if filter.Limit > 0 {
		limit = fmt.Sprintf(" limit %d", filter.Limit)
	}
	baseQuery := querySQL1
	rows, err := queryAuditRows(
		ctx,
		db,
		baseQuery+where+` order by e.time desc`+limit,
		args,
	)
	if err != nil {
		log.WarnContext(ctx, "query audit events failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []QueryRecord
	for rows.Next() {
		var record QueryRecord
		var checked, matched string
		var canBlock int
		if err := rows.Scan(&record.EventID, &record.SchemaVersion, &record.Time, &record.Level, &record.Message,
			&record.System, &record.SessionID, &record.TurnID, &record.EventName, &record.ToolUseID, &record.ToolName, &record.RawPayloadHash,
			&record.Operation.CWD, &record.Operation.EffectiveCWD, &record.Operation.Command, &record.Operation.FilePath,
			&record.Decision.Kind, &canBlock, &checked, &matched); err != nil {
			log.WarnContext(ctx, "scan audit event row failed", slog.String("path", path), slog.Any("err", err))
			return nil, fmt.Errorf("scan audit event row: %w", err)
		}
		record.Decision.CanBlock = canBlock != 0
		_ = json.Unmarshal([]byte(checked), &record.Decision.RulesChecked)
		_ = json.Unmarshal([]byte(matched), &record.Decision.RulesMatched)
		violations, err := sqliteViolations(ctx, db, record.EventID)
		if err != nil {
			return nil, err
		}
		record.Violations = violations
		record.Detail = auditstorage.ProjectDetail(0, 0, 0)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		log.WarnContext(ctx, "iterate audit event rows failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return out, nil
}

func auditReadOnlySQLiteDSN(path string) string {
	location := url.URL{Scheme: "file", Path: path}
	values := url.Values{}
	values.Set("mode", "ro")
	values.Set("_foreign_keys", "on")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "5000")
	location.RawQuery = values.Encode()
	return location.String()
}

func queryWhere(filter QueryFilter) (string, []queryArg) {
	var clauses []string
	var args []queryArg
	add := func(clause string, arg string) {
		clauses = append(clauses, clause)
		args = append(args, queryArg{Value: arg})
	}
	if !filter.Since.IsZero() {
		add("e.time >= ?", filter.Since.UTC().Format(time.RFC3339Nano))
	}
	if !filter.Until.IsZero() {
		add("e.time <= ?", filter.Until.UTC().Format(time.RFC3339Nano))
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
	if filter.Decision != "" {
		add("d.kind = ?", filter.Decision)
	}
	if filter.Rule != "" {
		add("exists (select 1 from violations v where v.event_id = e.event_id and v.rule = ?)", filter.Rule)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "where " + strings.Join(clauses, " and "), args
}

func queryAuditRows(ctx context.Context, db *sql.DB, query string, args []queryArg) (*sql.Rows, error) {
	log := slog.Default()
	var rows *sql.Rows
	var err error
	switch len(args) {
	case 0:
		rows, err = db.QueryContext(ctx, query)
	case 1:
		rows, err = db.QueryContext(ctx, query, args[0].Value)
	case 2:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value)
	case 3:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value)
	case 4:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value, args[3].Value)
	case 5:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value, args[3].Value, args[4].Value)
	case 6:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value, args[3].Value, args[4].Value, args[5].Value)
	case 7:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value, args[3].Value, args[4].Value, args[5].Value, args[6].Value)
	case 8:
		rows, err = db.QueryContext(ctx, query, args[0].Value, args[1].Value, args[2].Value, args[3].Value, args[4].Value, args[5].Value, args[6].Value, args[7].Value)
	default:
		err := errors.New("too many audit query filters")
		log.ErrorContext(ctx, "audit query argument limit exceeded", slog.Int("arg_count", len(args)), slog.Any("err", err))
		return nil, err
	}
	if err != nil {
		log.WarnContext(ctx, "query audit rows failed", slog.Int("arg_count", len(args)), slog.Any("err", err))
		return nil, fmt.Errorf("query audit rows: %w", err)
	}
	return rows, nil
}

func sqliteViolations(ctx context.Context, db *sql.DB, eventID string) ([]Violation, error) {
	log := slog.Default()
	rows, err := db.QueryContext(ctx, querySQL2, eventID)
	if err != nil {
		log.WarnContext(ctx, "query audit violations failed", slog.String("event_id", eventID), slog.Any("err", err))
		return nil, fmt.Errorf("query audit violations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Violation
	for rows.Next() {
		var v Violation
		if err := rows.Scan(&v.Rule, &v.Mode, &v.FieldPath, &v.FilePath, &v.Start, &v.End, &v.Message); err != nil {
			log.WarnContext(ctx, "scan audit violation row failed", slog.String("event_id", eventID), slog.Any("err", err))
			return nil, fmt.Errorf("scan audit violation row: %w", err)
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		log.WarnContext(ctx, "iterate audit violation rows failed", slog.String("event_id", eventID), slog.Any("err", err))
		return nil, fmt.Errorf("iterate audit violations: %w", err)
	}
	return out, nil
}

//go:embed query_1.sql
var querySQL1 string

//go:embed query_2.sql
var querySQL2 string
