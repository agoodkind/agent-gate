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
	events, err := querySQLite(cfg, filter)
	if err != nil {
		return nil, "sqlite", err
	}
	return events, "sqlite", nil
}

func querySQLite(cfg *config.Config, filter QueryFilter) ([]QueryRecord, error) {
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
	payloadColumns, err := auditPayloadProjectionColumns(ctx, db)
	if err != nil {
		return nil, err
	}
	const baseQuery = `select e.event_id, e.schema_version, e.time, e.level, e.message, e.system, e.session_id, e.turn_id, e.event_name, e.tool_use_id, e.tool_name, e.raw_payload_hash,
		coalesce(o.cwd, ''), coalesce(o.effective_cwd, ''), coalesce(o.command, ''), coalesce(o.file_path, ''),
		coalesce(d.kind, ''), coalesce(d.can_block, 0), coalesce(d.rules_checked_json, '[]'), coalesce(d.rules_matched_json, '[]'),`
	const queryFrom = `
		from events e
		left join operations o on o.event_id = e.event_id
		left join decisions d on d.event_id = e.event_id
		`
	rows, err := queryAuditRows(
		ctx,
		db,
		baseQuery+payloadColumns+queryFrom+where+` order by e.time desc`+limit,
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
		var payloadRecorded sql.NullInt64
		var payloadAvailable sql.NullInt64
		var stateChangedAt sql.NullString
		var protected sql.NullInt64
		if err := rows.Scan(&record.EventID, &record.SchemaVersion, &record.Time, &record.Level, &record.Message,
			&record.System, &record.SessionID, &record.TurnID, &record.EventName, &record.ToolUseID, &record.ToolName, &record.RawPayloadHash,
			&record.Operation.CWD, &record.Operation.EffectiveCWD, &record.Operation.Command, &record.Operation.FilePath,
			&record.Decision.Kind, &canBlock, &checked, &matched, &payloadRecorded,
			&payloadAvailable, &stateChangedAt, &protected); err != nil {
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
		record.Detail = projectAuditPayloadDetail(
			payloadRecorded, payloadAvailable, stateChangedAt, protected,
		)
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		log.WarnContext(ctx, "iterate audit event rows failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("iterate audit events: %w", err)
	}
	return out, nil
}

func auditPayloadProjectionColumns(ctx context.Context, database *sql.DB) (string, error) {
	hasRecordedState, err := auditColumnExists(
		ctx, database, "deferred_audit_outbox_entries", "payload_recorded",
	)
	if err != nil {
		return "", err
	}
	if hasRecordedState {
		return `
		(select payload_recorded from deferred_audit_outbox_entries payload_header
			where payload_header.audit_event_id = e.event_id
			order by payload_header.receipt_id desc limit 1),
		(select case when payload_header.payload_available = 1 and exists (
				select 1 from deferred_audit_outbox_entry_details payload_detail
				where payload_detail.receipt_id = payload_header.receipt_id
					and payload_detail.entry_index = payload_header.entry_index
			) then 1 else 0 end
			from deferred_audit_outbox_entries payload_header
			where payload_header.audit_event_id = e.event_id
			order by payload_header.receipt_id desc limit 1),
		(select payload_state_changed_at from deferred_audit_outbox_entries payload_header
			where payload_header.audit_event_id = e.event_id
			order by payload_header.receipt_id desc limit 1),
		exists (
			select 1 from deferred_audit_outbox_entries protected_entry
			join deferred_audit_outbox protected_outbox
				on protected_outbox.receipt_id = protected_entry.receipt_id
			where protected_entry.audit_event_id = e.event_id
				and (protected_outbox.state = 'pending'
					or protected_entry.delivered_at is null)
				and exists (
					select 1 from deferred_audit_outbox_entry_details protected_detail
					where protected_detail.receipt_id = protected_entry.receipt_id
						and protected_detail.entry_index = protected_entry.entry_index
				)
		)`, nil
	}
	hasCompatibilityPayload, err := auditColumnExists(
		ctx, database, "deferred_audit_outbox_entries", "payload_json",
	)
	if err != nil {
		return "", err
	}
	if hasCompatibilityPayload {
		return `
		(select 1 from deferred_audit_outbox_entries compatibility_entry
			where compatibility_entry.audit_event_id = e.event_id
				and compatibility_entry.payload_json is not null limit 1),
		(select 1 from deferred_audit_outbox_entries compatibility_entry
			where compatibility_entry.audit_event_id = e.event_id
				and compatibility_entry.payload_json is not null limit 1),
		null,
		0`, nil
	}
	return "cast(null as integer), cast(null as integer), cast(null as text), 0", nil
}

func auditColumnExists(
	ctx context.Context,
	database *sql.DB,
	tableName string,
	columnName string,
) (bool, error) {
	var exists int
	err := database.QueryRowContext(ctx, `
		select 1 from pragma_table_info(?) where name = ?
	`, tableName, columnName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		log := slog.Default()
		log.WarnContext(
			ctx,
			"query audit column metadata failed",
			slog.String("table", tableName),
			slog.String("column", columnName),
			slog.Any("err", err),
		)
		return false, fmt.Errorf("query audit column metadata: %w", err)
	}
	return exists == 1, nil
}

func projectAuditPayloadDetail(
	payloadRecorded sql.NullInt64,
	payloadAvailable sql.NullInt64,
	stateChangedAt sql.NullString,
	protected sql.NullInt64,
) auditstorage.DetailProjection {
	if !payloadRecorded.Valid {
		return auditstorage.ProjectDetail(nil, nil, nil, auditstorage.DetailStateAvailable, "", nil)
	}
	requested := []auditstorage.DetailClass{auditstorage.DetailClassDeferredAuditPayload}
	recorded := make([]auditstorage.DetailClass, 0, 1)
	if payloadRecorded.Int64 != 0 {
		recorded = append(recorded, auditstorage.DetailClassDeferredAuditPayload)
	}
	available := make([]auditstorage.DetailClass, 0, 1)
	if payloadAvailable.Int64 != 0 {
		available = append(available, auditstorage.DetailClassDeferredAuditPayload)
	}
	storedState := auditstorage.DetailStateAvailable
	if payloadRecorded.Int64 == 0 {
		storedState = auditstorage.DetailStateNotRecorded
	}
	if payloadRecorded.Int64 != 0 && payloadAvailable.Int64 == 0 {
		storedState = auditstorage.DetailStateExpired
	}
	if payloadRecorded.Int64 == 0 && payloadAvailable.Int64 != 0 {
		storedState = auditstorage.DetailStateProtected
	}
	protectedClasses := make([]auditstorage.DetailClass, 0, 1)
	if protected.Valid && protected.Int64 != 0 {
		protectedClasses = append(
			protectedClasses, auditstorage.DetailClassDeferredAuditPayload,
		)
	}
	return auditstorage.ProjectDetail(
		recorded, available, requested, storedState, stateChangedAt.String,
		protectedClasses,
	)
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
