package audit

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
)

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

func Query(cfg *config.Config, filter QueryFilter) ([]Event, string, error) {
	prefer := "sqlite"
	if cfg != nil {
		prefer = cfg.AuditQueryPrefer()
	}
	if prefer == "sqlite" {
		if events, err := querySQLite(cfg, filter); err == nil {
			return events, "sqlite", nil
		}
		return queryJSONLWithSource(cfg, filter)
	}
	if events, source, err := queryJSONLWithSource(cfg, filter); err == nil {
		return events, source, nil
	}
	events, err := querySQLite(cfg, filter)
	return events, "sqlite", err
}

func queryJSONLWithSource(cfg *config.Config, filter QueryFilter) ([]Event, string, error) {
	events, err := queryJSONL(cfg, filter)
	return events, "jsonl", err
}

func querySQLite(cfg *config.Config, filter QueryFilter) ([]Event, error) {
	path := config.DefaultAuditSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	where, args := queryWhere(filter)
	limit := ""
	if filter.Limit > 0 {
		limit = fmt.Sprintf(" limit %d", filter.Limit)
	}
	rows, err := db.Query(`select e.event_id, e.schema_version, e.time, e.level, e.message, e.system, e.session_id, e.turn_id, e.event_name, e.tool_use_id, e.tool_name, e.raw_payload_hash,
		coalesce(o.cwd, ''), coalesce(o.effective_cwd, ''), coalesce(o.command, ''), coalesce(o.file_path, ''),
		coalesce(d.kind, ''), coalesce(d.can_block, 0), coalesce(d.rules_checked_json, '[]'), coalesce(d.rules_matched_json, '[]')
		from events e
		left join operations o on o.event_id = e.event_id
		left join decisions d on d.event_id = e.event_id
		`+where+` order by e.time desc`+limit, args...)
	if err != nil {
		return nil, err
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
			return nil, err
		}
		event.Decision.CanBlock = canBlock != 0
		_ = json.Unmarshal([]byte(checked), &event.Decision.RulesChecked)
		_ = json.Unmarshal([]byte(matched), &event.Decision.RulesMatched)
		violations, err := sqliteViolations(db, event.EventID)
		if err != nil {
			return nil, err
		}
		event.Violations = violations
		out = append(out, event)
	}
	return out, rows.Err()
}

func queryWhere(filter QueryFilter) (string, []any) {
	var clauses []string
	var args []any
	add := func(clause string, arg any) {
		clauses = append(clauses, clause)
		args = append(args, arg)
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
		clauses = append(clauses, "exists (select 1 from violations v where v.event_id = e.event_id and v.rule = ?)")
		args = append(args, filter.Rule)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "where " + strings.Join(clauses, " and "), args
}

func sqliteViolations(db *sql.DB, eventID string) ([]Violation, error) {
	rows, err := db.Query(`select rule, mode, field_path, file_path, start, end, message from violations where event_id = ? order by id`, eventID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Violation
	for rows.Next() {
		var v Violation
		if err := rows.Scan(&v.Rule, &v.Mode, &v.FieldPath, &v.FilePath, &v.Start, &v.End, &v.Message); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func queryJSONL(cfg *config.Config, filter QueryFilter) ([]Event, error) {
	dir := config.DefaultAuditEventsDir()
	if cfg != nil {
		dir = cfg.AuditEventsDir()
	}
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	var out []Event
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(path) != "events.jsonl" {
			return err
		}
		events, err := scanEventFile(path, filter)
		if err != nil {
			return err
		}
		out = append(out, events...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortEventsDesc(out)
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func scanEventFile(path string, filter QueryFilter) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var out []Event
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		if eventMatches(event, filter) {
			out = append(out, event)
		}
	}
	return out, scanner.Err()
}

func eventMatches(event Event, filter QueryFilter) bool {
	ts, _ := time.Parse(time.RFC3339Nano, event.Time)
	if !filter.Since.IsZero() && ts.Before(filter.Since) {
		return false
	}
	if !filter.Until.IsZero() && ts.After(filter.Until) {
		return false
	}
	if filter.System != "" && event.System != filter.System {
		return false
	}
	if filter.SessionID != "" && event.SessionID != filter.SessionID {
		return false
	}
	if filter.EventName != "" && event.EventName != filter.EventName {
		return false
	}
	if filter.ToolName != "" && event.ToolName != filter.ToolName {
		return false
	}
	if filter.Decision != "" && event.Decision.Kind != filter.Decision {
		return false
	}
	if filter.Rule != "" {
		for _, v := range event.Violations {
			if v.Rule == filter.Rule {
				return true
			}
		}
		return false
	}
	return true
}

func sortEventsDesc(events []Event) {
	sort.Slice(events, func(i, j int) bool {
		return events[i].Time > events[j].Time
	})
}
