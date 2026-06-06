package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
)

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
func Query(cfg *config.Config, filter QueryFilter) ([]Event, string, error) {
	events, err := querySQLite(cfg, filter)
	if err != nil {
		return nil, "sqlite", err
	}
	return events, "sqlite", nil
}

func querySQLite(cfg *config.Config, filter QueryFilter) ([]Event, error) {
	ctx := context.Background()
	log := slog.Default()
	path := config.DefaultAuditSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	if _, err := os.Stat(path); err != nil {
		log.WarnContext(ctx, "stat audit sqlite path failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("stat audit sqlite path: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
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
	const baseQuery = `select e.event_id, e.schema_version, e.time, e.level, e.message, e.system, e.session_id, e.turn_id, e.event_name, e.tool_use_id, e.tool_name, e.raw_payload_hash,
		coalesce(o.cwd, ''), coalesce(o.effective_cwd, ''), coalesce(o.command, ''), coalesce(o.file_path, ''),
		coalesce(d.kind, ''), coalesce(d.can_block, 0), coalesce(d.rules_checked_json, '[]'), coalesce(d.rules_matched_json, '[]')
		from events e
		left join operations o on o.event_id = e.event_id
		left join decisions d on d.event_id = e.event_id
		`
	// where + limit are derived from a closed enum filter and a positive int;
	// they cannot carry user-supplied SQL fragments, so concatenation is safe
	// here. Placeholders are still used for filter values via args.
	rows, err := queryAuditRows(ctx, db, baseQuery+where+` order by e.time desc`+limit, args)
	if err != nil {
		log.WarnContext(ctx, "query audit events failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("query audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Event
	for rows.Next() {
		var event Event
		var checked, matched string
		var canBlock int
		if err := rows.Scan(&event.EventID, &event.SchemaVersion, &event.Time, &event.Level, &event.Message,
			&event.System, &event.SessionID, &event.TurnID, &event.EventName, &event.ToolUseID, &event.ToolName, &event.RawPayloadHash,
			&event.Operation.CWD, &event.Operation.EffectiveCWD, &event.Operation.Command, &event.Operation.FilePath,
			&event.Decision.Kind, &canBlock, &checked, &matched); err != nil {
			log.WarnContext(ctx, "scan audit event row failed", slog.String("path", path), slog.Any("err", err))
			return nil, fmt.Errorf("scan audit event row: %w", err)
		}
		event.Decision.CanBlock = canBlock != 0
		_ = json.Unmarshal([]byte(checked), &event.Decision.RulesChecked)
		_ = json.Unmarshal([]byte(matched), &event.Decision.RulesMatched)
		violations, err := sqliteViolations(ctx, db, event.EventID)
		if err != nil {
			return nil, err
		}
		event.Violations = violations
		out = append(out, event)
	}
	if err := rows.Err(); err != nil {
		log.WarnContext(ctx, "iterate audit event rows failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return out, nil
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
	rows, err := db.QueryContext(ctx, `select rule, mode, field_path, file_path, start, end, message from violations where event_id = ? order by id`, eventID)
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
