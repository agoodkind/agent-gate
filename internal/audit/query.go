package audit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

// QueryRecord is the public query projection of an audit event.
type QueryRecord struct {
	BucketID string `json:"bucket_id"`
	Event
	Detail auditstorage.DetailProjection `json:"detail"`
}

// QueryFilter narrows the set of audit events returned by [QueryReadOnly].
type QueryFilter struct {
	BucketID  string
	EventID   string
	Offset    int
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

// QueryReadOnly returns a globally ordered page from retained audit buckets.
func QueryReadOnly(ctx context.Context, cfg *config.Config, filter QueryFilter) ([]QueryRecord, string, error) {
	if filter.Limit == 0 {
		filter.Limit = 100
	}
	records := make([]QueryRecord, 0)
	err := Walk(ctx, cfg, filter, func(record QueryRecord) error { records = append(records, record); return nil })
	return records, "sqlite", err
}

// Walk streams retained audit events; zero limit visits every matching event.
func Walk(ctx context.Context, cfg *config.Config, filter QueryFilter, yield func(QueryRecord) error) error {
	if err := auditstorage.ValidatePage(filter.Limit, filter.Offset); err != nil {
		return storageError("read retained history", err)
	}
	set, err := cfg.ReadAuditHistory(ctx, filter.BucketID)
	if err != nil {
		return storageError("read retained history", err)
	}
	defer func() { _ = set.Close() }()
	err = auditstorage.Merge(ctx, set, filter.Limit, filter.Offset,
		func(handle *auditstorage.BucketHandle, cursor *auditstorage.QuerySummary) ([]auditstorage.QuerySummary, error) {
			where, args := queryWhere(filter)
			if cursor != nil {
				if where == "" {
					where = " where "
				} else {
					where += " and "
				}
				where += auditKeysetSQL
				args = append(args, queryArg{Value: cursor.Time}, queryArg{Value: cursor.ID})
			}
			rows, err := queryAuditRows(ctx, handle.Database, auditSummarySQL+where+auditPageSQL, args)
			if err != nil {
				return nil, storageError("read query rows", err)
			}
			defer func() { _ = rows.Close() }()
			var result []auditstorage.QuerySummary
			for rows.Next() {
				var row auditstorage.QuerySummary
				if err := rows.Scan(&row.ID, &row.Time); err != nil {
					return nil, storageError("read query rows", err)
				}
				result = append(result, row)
			}
			return result, rows.Err()
		},
		func(handle *auditstorage.BucketHandle, row auditstorage.QuerySummary) error {
			records, err := queryRecords(ctx, handle.Database, selectedAuditFilter(row.ID))
			if err != nil {
				return storageError("read retained history", err)
			}
			if len(records) != 1 {
				return fmt.Errorf("audit event %q disappeared during read", row.ID)
			}
			records[0].BucketID = handle.Bucket.ID
			return yield(records[0])
		})
	if err != nil {
		return storageError("walk retained history", err)
	}
	return nil
}

func queryRecords(ctx context.Context, db *sql.DB, filter QueryFilter) ([]QueryRecord, error) {
	log := slog.Default()
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
		log.WarnContext(ctx, "query audit events failed", slog.Any("err", err))
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
			log.WarnContext(ctx, "scan audit event row failed", slog.Any("err", err))
			return nil, fmt.Errorf("scan audit event row: %w", err)
		}
		record.Decision.CanBlock = canBlock != 0
		_ = json.Unmarshal([]byte(checked), &record.Decision.RulesChecked)
		_ = json.Unmarshal([]byte(matched), &record.Decision.RulesMatched)
		record.Detail = auditstorage.ProjectDetail(0, 0, 0)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		log.WarnContext(ctx, "iterate audit event rows failed", slog.Any("err", err))
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, storageError("read query rows", err)
	}
	for index := range out {
		violations, err := sqliteViolations(ctx, db, out[index].EventID)
		if err != nil {
			return nil, storageError("read query rows", err)
		}
		out[index].Violations = violations
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
	if filter.EventID != "" {
		add("e.event_id = ?", filter.EventID)
	}
	if !filter.Since.IsZero() {
		add("e.time >= ?", auditstorage.FormatTime(filter.Since))
	}
	if !filter.Until.IsZero() {
		add("e.time <= ?", auditstorage.FormatTime(filter.Until))
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
	values := make([]any, 0, len(args))
	for _, arg := range args {
		values = append(values, arg.Value)
	}
	rows, err := db.QueryContext(ctx, query, values...)
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

//go:embed query_summary.sql
var auditSummarySQL string

//go:embed query_page.sql
var auditPageSQL string

//go:embed query_keyset.sql
var auditKeysetSQL string

func selectedAuditFilter(eventID string) QueryFilter {
	var filter QueryFilter
	filter.EventID, filter.Limit = eventID, 1
	return filter
}
