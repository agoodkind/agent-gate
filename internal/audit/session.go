package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	expirable "github.com/hashicorp/golang-lru/v2/expirable"
	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
)

const (
	dedupCacheSize    = 4096
	dedupTTL          = 30 * time.Second
	schemaVersion     = 1
	defaultQueueLimit = 8192
	dropLogInterval   = 5 * time.Second
)

type auditMessage string

const (
	auditMessageAllowed        auditMessage = "hook.allowed"
	auditMessageAuditViolation auditMessage = "hook.audit_violation"
	auditMessageBlocked        auditMessage = "hook.blocked"
)

type auditLevelName string

const (
	auditLevelDebug   auditLevelName = "debug"
	auditLevelErr     auditLevelName = "err"
	auditLevelError   auditLevelName = "error"
	auditLevelWarn    auditLevelName = "warn"
	auditLevelWarning auditLevelName = "warning"
)

// EventLogger is the audit event sink shared by the daemon and the CLI.
// It owns a dedup cache, a worker goroutine that flushes batched writes,
// and zero or more configured outputs (JSONL, SQLite).
type EventLogger struct {
	minLevel slog.Level
	dedup    *expirable.LRU[string, struct{}]
	outputs  []eventSink
	rawHash  bool
	enabled  bool

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []eventWrite
	limit    int
	dropped  uint64
	lastDrop time.Time
	stopping bool

	wg       sync.WaitGroup
	log      *slog.Logger
	sharedDB *sql.DB
}

type eventWrite struct {
	event      Event
	rawPayload string
}

// LoggerOptions tunes queue behavior for tests and high-throughput daemon use.
// SharedDB, when set, makes the SQLite sink write through an existing handle
// (the intake store's) instead of opening its own, so audit and intake writes
// share one serialized connection pool.
type LoggerOptions struct {
	QueueLimit int
	SharedDB   *sql.DB
}

// Event is one normalized audit record. It is the canonical schema written
// to all configured outputs.
type Event struct {
	EventID        string      `json:"event_id"`
	SchemaVersion  int         `json:"schema_version"`
	Time           string      `json:"time"`
	Level          string      `json:"level"`
	Message        string      `json:"message"`
	System         string      `json:"system"`
	SessionID      string      `json:"session_id"`
	TurnID         string      `json:"turn_id,omitempty"`
	EventName      string      `json:"event_name"`
	ToolUseID      string      `json:"tool_use_id,omitempty"`
	ToolName       string      `json:"tool_name,omitempty"`
	Operation      Operation   `json:"operation,omitzero"`
	Decision       Decision    `json:"decision,omitzero"`
	Violations     []Violation `json:"violations,omitempty"`
	RawPayloadHash string      `json:"raw_payload_hash,omitempty"`
}

// Operation captures the working-directory and command context of an event.
type Operation struct {
	CWD          string `json:"cwd,omitempty"`
	EffectiveCWD string `json:"effective_cwd,omitempty"`
	Command      string `json:"command,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

// Decision captures the rule-engine verdict for an event.
type Decision struct {
	Kind         string   `json:"kind,omitempty"`
	CanBlock     bool     `json:"can_block,omitempty"`
	RulesChecked []string `json:"rules_checked,omitempty"`
	RulesMatched []string `json:"rules_matched,omitempty"`
}

// Violation describes a single rule match recorded against an event.
type Violation struct {
	Rule      string `json:"rule"`
	Mode      string `json:"mode"`
	FieldPath string `json:"field_path,omitempty"`
	FilePath  string `json:"file_path,omitempty"`
	Start     int    `json:"start,omitempty"`
	End       int    `json:"end,omitempty"`
	Message   string `json:"message,omitempty"`
}

type eventSink interface {
	Write(Event, string) error
	Close() error
}

// NewEventLoggerWithOptions constructs an [EventLogger] with explicit queue
// tuning. Zero-valued options select production defaults.
func NewEventLoggerWithOptions(ctx context.Context, cfg *config.Config, log *slog.Logger, options LoggerOptions) (*EventLogger, error) {
	if log == nil {
		log = slog.Default()
	}
	level := ""
	if cfg != nil {
		level = cfg.AuditLevel()
	}
	queueLimit := options.QueueLimit
	if queueLimit <= 0 {
		queueLimit = defaultQueueLimit
	}
	el := new(EventLogger)
	el.minLevel = parseLevel(level)
	el.enabled = true
	el.limit = queueLimit
	el.log = log
	el.sharedDB = options.SharedDB
	el.cond = sync.NewCond(&el.mu)
	el.dedup = expirable.NewLRU[string, struct{}](dedupCacheSize, nil, dedupTTL)

	if cfg != nil && !cfg.AuditEnabled() {
		el.enabled = false
	}
	if el.enabled {
		if err := el.configureOutputs(ctx, cfg, log); err != nil {
			return nil, err
		}
	}
	if len(el.outputs) == 0 {
		el.enabled = false
	}

	if el.enabled {
		el.wg.Add(1)
		go func() {
			defer func() {
				if recovered := recover(); recovered != nil {
					el.log.ErrorContext(ctx, "audit worker panic recovered", slog.Any("err", recovered))
				}
				el.wg.Done()
			}()
			el.worker()
		}()
	}
	return el, nil
}

// Enabled reports whether the logger has at least one active output.
func (el *EventLogger) Enabled() bool {
	return el != nil && el.enabled
}

func (el *EventLogger) configureOutputs(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	// SQLite is the sole audit sink. Decisions and violations are persisted to
	// the same audit.db that backs intake; the operational agent-gate.jsonl log
	// is for debugging agent-gate itself, not audit output. When a shared handle
	// is supplied the sink writes through the intake store's connection pool so
	// the two writers serialize instead of contending.
	el.rawHash = true
	if el.sharedDB != nil {
		s, err := newSQLiteEventSinkFromDB(ctx, el.sharedDB, log)
		if err != nil {
			return err
		}
		el.outputs = append(el.outputs, s)
		return nil
	}
	sqlitePath := config.DefaultAuditSQLitePath()
	if cfg != nil {
		sqlitePath = cfg.AuditSQLitePath()
	}
	s, err := newSQLiteEventSink(ctx, sqlitePath, log)
	if err != nil {
		return err
	}
	el.outputs = append(el.outputs, s)
	return nil
}

// Log enqueues a normalized audit event for asynchronous write to all
// configured outputs. Calls return without waiting for I/O; the call is
// also a no-op when the receiver is nil or the level is filtered out.
func (el *EventLogger) Log(system, sessionID, eventName, level, msg string, attrs Attrs) {
	if el == nil || !el.enabled || !el.shouldLog(level) {
		return
	}
	if !el.hasQueueCapacity() {
		el.recordDrop(system, sessionID, eventName, msg)
		return
	}

	event := normalizeEvent(system, sessionID, eventName, level, msg, attrs)
	fingerprint := dedupFingerprint(event, attrs)
	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		return
	}
	if len(el.queue) >= el.limit {
		el.mu.Unlock()
		el.recordDrop(system, sessionID, eventName, msg)
		return
	}
	if _, seen := el.dedup.Get(fingerprint); seen {
		el.mu.Unlock()
		return
	}
	el.dedup.Add(fingerprint, struct{}{})
	event.EventID = "evt_" + fingerprint[:32]

	rawPayload := ""
	if value, ok := attrs["raw_payload"]; ok {
		rawPayload = value.String()
	}
	if rawPayload != "" && el.rawHash {
		event.RawPayloadHash = payloadHash(rawPayload)
	}

	el.queue = append(el.queue, eventWrite{event: event, rawPayload: rawPayload})
	el.cond.Signal()
	el.mu.Unlock()
}

func (el *EventLogger) hasQueueCapacity() bool {
	el.mu.Lock()
	defer el.mu.Unlock()
	return !el.stopping && len(el.queue) < el.limit
}

func (el *EventLogger) recordDrop(system, sessionID, eventName, msg string) {
	now := auditNow()
	el.mu.Lock()
	el.dropped++
	dropped := el.dropped
	if !el.lastDrop.IsZero() && now.Sub(el.lastDrop) < dropLogInterval {
		el.mu.Unlock()
		return
	}
	el.lastDrop = now
	queueDepth := len(el.queue)
	queueLimit := el.limit
	el.mu.Unlock()

	el.log.Warn("audit queue full; dropping event",
		"system", system,
		"session_id", sessionID,
		"event", eventName,
		"msg", msg,
		"queue_depth", queueDepth,
		"queue_limit", queueLimit,
		"dropped", dropped,
	)
}

// Close stops the background worker, drains the queue to all configured
// outputs, and releases their resources. Close is idempotent.
func (el *EventLogger) Close() error {
	if el == nil {
		return nil
	}
	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		return nil
	}
	el.stopping = true
	el.cond.Broadcast()
	el.mu.Unlock()

	el.wg.Wait()
	for _, output := range el.outputs {
		_ = output.Close()
	}
	return nil
}

func (el *EventLogger) worker() {
	for {
		el.mu.Lock()
		for len(el.queue) == 0 && !el.stopping {
			el.cond.Wait()
		}
		batch := el.queue
		el.queue = nil
		stopping := el.stopping
		el.mu.Unlock()

		for _, w := range batch {
			for _, output := range el.outputs {
				if err := output.Write(w.event, w.rawPayload); err != nil {
					el.log.Warn("audit output write failed", "event_id", w.event.EventID, "err", err)
				}
			}
		}

		if stopping {
			el.mu.Lock()
			remaining := el.queue
			el.queue = nil
			el.mu.Unlock()
			for _, w := range remaining {
				for _, output := range el.outputs {
					if err := output.Write(w.event, w.rawPayload); err != nil {
						el.log.Warn("audit output write failed", "event_id", w.event.EventID, "err", err)
					}
				}
			}
			return
		}
	}
}

var systemAuditClock auditClock = realAuditClock{}

type auditClock interface {
	Now() time.Time
}

type realAuditClock struct{}

var realAuditNow = time.Now

func (realAuditClock) Now() time.Time {
	return realAuditNow()
}

func normalizeEvent(system, sessionID, eventName, level, msg string, attrs Attrs) Event {
	now := auditNow().UTC().Format(time.RFC3339Nano)
	event := Event{
		EventID:       "",
		SchemaVersion: schemaVersion,
		Time:          now,
		Level:         level,
		Message:       msg,
		System:        stringAttr(attrs, "system", system),
		SessionID:     stringAttr(attrs, "session_id", sessionID),
		TurnID:        stringAttr(attrs, "turn_id", ""),
		EventName:     stringAttr(attrs, "event", eventName),
		ToolUseID:     stringAttr(attrs, "tool_use_id", ""),
		ToolName:      stringAttr(attrs, "tool_name", ""),
		Operation: Operation{
			CWD:          stringAttr(attrs, "cwd", ""),
			EffectiveCWD: stringAttr(attrs, "effective_cwd", ""),
			Command:      firstStringAttr(attrs, "ti_command", "command"),
			FilePath:     firstStringAttr(attrs, "file_path", "ti_file_path"),
		},
		Decision: Decision{
			Kind:         stringAttr(attrs, "decision", decisionFromMessage(msg)),
			CanBlock:     false,
			RulesChecked: stringSliceAttr(attrs, "rules_checked"),
			RulesMatched: stringSliceAttr(attrs, "blocking_rules"),
		},
		Violations:     nil,
		RawPayloadHash: "",
	}
	if event.System == "" {
		event.System = "unknown"
	}
	if event.SessionID == "" {
		event.SessionID = "_no-session"
	}
	if event.EventName == "" {
		event.EventName = "_unknown"
	}
	event.Decision.CanBlock = event.Decision.Kind == "block"
	event.Violations = violationsFromAttrs(event.Decision.RulesMatched, event.Decision.Kind, attrs)
	return event
}

func auditNow() time.Time {
	return systemAuditClock.Now()
}

func decisionFromMessage(msg string) string {
	switch auditMessage(msg) {
	case auditMessageAllowed:
		return "allow"
	case auditMessageBlocked:
		return "block"
	case auditMessageAuditViolation:
		return "audit_only"
	default:
		return ""
	}
}

func violationsFromAttrs(rules []string, decision string, attrs Attrs) []Violation {
	if len(rules) == 0 {
		return nil
	}
	mode := "blocking"
	if decision == "audit_only" {
		mode = "audit_only"
	}
	message := stringAttr(attrs, "violation_message", "")
	out := make([]Violation, 0, len(rules))
	for _, rule := range rules {
		out = append(out, Violation{Rule: rule, Mode: mode, FieldPath: "", FilePath: "", Start: 0, End: 0, Message: message})
	}
	return out
}

func dedupFingerprint(event Event, attrs Attrs) string {
	stable := make(Attrs, len(attrs)+12)
	for key, value := range attrs {
		if key == "time" {
			continue
		}
		stable[key] = value
	}
	stable["system"] = NewStringValue(event.System)
	stable["session_id"] = NewStringValue(event.SessionID)
	stable["event"] = NewStringValue(event.EventName)
	stable["level"] = NewStringValue(event.Level)
	stable["msg"] = NewStringValue(event.Message)

	keys := make([]string, 0, len(stable))
	for key := range stable {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	h := sha256.New()
	for _, key := range keys {
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{'='})
		bytes := stable[key].JSONBytes()
		_, _ = h.Write(bytes)
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (el *EventLogger) shouldLog(level string) bool {
	return parseLevel(level) >= el.minLevel
}

func parseLevel(s string) slog.Level {
	switch auditLevelName(strings.ToLower(strings.TrimSpace(s))) {
	case auditLevelDebug:
		return slog.LevelDebug
	case auditLevelWarn, auditLevelWarning:
		return slog.LevelWarn
	case auditLevelError, auditLevelErr:
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func stringAttr(attrs Attrs, key, fallback string) string {
	if attrs == nil {
		return fallback
	}
	if value, ok := attrs[key]; ok {
		if v := value.String(); v != "" {
			return v
		}
	}
	return fallback
}

func firstStringAttr(attrs Attrs, keys ...string) string {
	for _, key := range keys {
		if v := stringAttr(attrs, key, ""); v != "" {
			return v
		}
	}
	return ""
}

func stringSliceAttr(attrs Attrs, key string) []string {
	if attrs == nil {
		return nil
	}
	value, ok := attrs[key]
	if !ok {
		return nil
	}
	return value.Strings()
}

func payloadHash(rawPayload string) string {
	sum := sha256.Sum256([]byte(rawPayload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

type sqliteEventSink struct {
	db     *sql.DB
	log    *slog.Logger
	ownsDB bool
}

func newSQLiteEventSink(ctx context.Context, path string, log *slog.Logger) (*sqliteEventSink, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.WarnContext(ctx, "create audit sqlite dir failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("create audit sqlite dir: %w", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		log.WarnContext(ctx, "open audit sqlite db failed", slog.String("path", path), slog.Any("err", err))
		return nil, fmt.Errorf("open audit sqlite db: %w", err)
	}
	// The audit sink shares audit.db with the durable intake store (two separate
	// connection pools to one WAL file). Match the intake store's single
	// serialized connection so the two writers wait on busy_timeout instead of
	// failing with SQLITE_BUSY when they contend, for example during the startup
	// intake replay that drives audit writes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &sqliteEventSink{db: db, log: log, ownsDB: true}
	if err := s.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// newSQLiteEventSinkFromDB builds a sink that writes through an already-open
// shared handle (the intake store's). It does not own the handle, so Close
// leaves it open for the owner to close. Sharing one pool serializes audit and
// intake writes and removes their cross-pool lock contention.
func newSQLiteEventSinkFromDB(ctx context.Context, db *sql.DB, log *slog.Logger) (*sqliteEventSink, error) {
	if log == nil {
		log = slog.Default()
	}
	s := &sqliteEventSink{db: db, log: log, ownsDB: false}
	if err := s.init(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *sqliteEventSink) init(ctx context.Context) error {
	stmts := []string{
		`pragma busy_timeout = 5000`,
		`pragma journal_mode = wal`,
		`create table if not exists events (
			event_id text primary key,
			schema_version integer,
			time text,
			level text,
			message text,
			system text,
			session_id text,
			turn_id text,
			event_name text,
			tool_use_id text,
			tool_name text,
			raw_payload_hash text
		)`,
		`create table if not exists operations (
			event_id text primary key,
			cwd text,
			effective_cwd text,
			command text,
			file_path text
		)`,
		`create table if not exists decisions (
			event_id text primary key,
			kind text,
			can_block integer,
			rules_checked_json text,
			rules_matched_json text
		)`,
		`create table if not exists violations (
			id integer primary key autoincrement,
			event_id text,
			rule text,
			mode text,
			field_path text,
			file_path text,
			start integer,
			end integer,
			message text
		)`,
		`create index if not exists events_time_idx on events(time)`,
		`create index if not exists events_system_time_idx on events(system, time)`,
		`create index if not exists events_session_time_idx on events(session_id, time)`,
		`create index if not exists events_tool_time_idx on events(tool_name, time)`,
		`create index if not exists events_event_name_time_idx on events(event_name, time)`,
		`create index if not exists decisions_kind_idx on decisions(kind)`,
		`create index if not exists violations_rule_idx on violations(rule)`,
		`create index if not exists violations_mode_idx on violations(mode)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			s.log.WarnContext(ctx, "init audit sqlite schema failed", slog.Any("err", err))
			return fmt.Errorf("init audit sqlite schema: %w", err)
		}
	}
	return nil
}

func (s *sqliteEventSink) Write(event Event, _ string) error {
	ctx := context.Background()
	checked, err := json.Marshal(event.Decision.RulesChecked)
	if err != nil {
		s.log.Warn("marshal audit rules_checked failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("marshal rules_checked: %w", err)
	}
	matched, err := json.Marshal(event.Decision.RulesMatched)
	if err != nil {
		s.log.Warn("marshal audit rules_matched failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("marshal rules_matched: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		s.log.Warn("begin audit tx failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("begin audit tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `insert or ignore into events values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.SchemaVersion, event.Time, event.Level, event.Message, event.System,
		event.SessionID, event.TurnID, event.EventName, event.ToolUseID, event.ToolName, event.RawPayloadHash,
	); err != nil {
		s.log.Warn("insert audit event failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("insert audit event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `insert or ignore into operations values (?, ?, ?, ?, ?)`,
		event.EventID, event.Operation.CWD, event.Operation.EffectiveCWD, event.Operation.Command, event.Operation.FilePath,
	); err != nil {
		s.log.Warn("insert audit operation failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("insert audit operation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `insert or ignore into decisions values (?, ?, ?, ?, ?)`,
		event.EventID, event.Decision.Kind, boolInt(event.Decision.CanBlock), string(checked), string(matched),
	); err != nil {
		s.log.Warn("insert audit decision failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("insert audit decision: %w", err)
	}
	for _, v := range event.Violations {
		if _, err := tx.ExecContext(ctx, `insert into violations (event_id, rule, mode, field_path, file_path, start, end, message) values (?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, v.Rule, v.Mode, v.FieldPath, v.FilePath, v.Start, v.End, v.Message,
		); err != nil {
			s.log.Warn("insert audit violation failed", slog.String("event_id", event.EventID), slog.String("rule", v.Rule), slog.Any("err", err))
			return fmt.Errorf("insert audit violation: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		s.log.Warn("commit audit tx failed", slog.String("event_id", event.EventID), slog.Any("err", err))
		return fmt.Errorf("commit audit tx: %w", err)
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *sqliteEventSink) Close() error {
	if s == nil || s.db == nil || !s.ownsDB {
		return nil
	}
	if err := s.db.Close(); err != nil {
		s.log.Warn("close audit sqlite db failed", slog.Any("err", err))
		return fmt.Errorf("close audit sqlite db: %w", err)
	}
	return nil
}

// AttrsFromSlog flattens a slice of [slog.Attr] into the audit attribute
// map, recursing into [slog.KindGroup] values to produce dotted keys.
func AttrsFromSlog(attrs []slog.Attr) Attrs {
	out := make(Attrs, len(attrs))
	for _, a := range attrs {
		flattenAttr("", a, out)
	}
	return out
}

func flattenAttr(prefix string, attr slog.Attr, out Attrs) {
	key := attr.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	value := attr.Value.Resolve()
	switch value.Kind() {
	case slog.KindGroup:
		for _, sub := range value.Group() {
			flattenAttr(key, sub, out)
		}
	case slog.KindString:
		out[key] = NewStringValue(value.String())
	case slog.KindBool:
		out[key] = NewBoolValue(value.Bool())
	case slog.KindInt64:
		out[key] = NewIntValue(value.Int64())
	case slog.KindUint64:
		if u := value.Uint64(); u <= math.MaxInt64 {
			out[key] = NewIntValue(int64(u))
		}
	case slog.KindFloat64:
		out[key] = NewFloatValue(value.Float64())
	case slog.KindDuration, slog.KindTime, slog.KindLogValuer:
		out[key] = NewStringValue(value.String())
	case slog.KindAny:
		switch stringsValue := value.Any().(type) {
		case []string:
			out[key] = NewStringSliceValue(stringsValue)
		case []Violation:
			out[key] = NewViolationSliceValue(stringsValue)
		}
	}
}

var _ = context.Background
