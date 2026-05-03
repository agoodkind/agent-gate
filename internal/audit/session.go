package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	expirable "github.com/hashicorp/golang-lru/v2/expirable"
	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
)

const (
	eventLogCacheSize = 16
	dedupCacheSize    = 4096
	dedupTTL          = 30 * time.Second
	schemaVersion     = 1
)

type EventLogger struct {
	minLevel slog.Level
	dedup    *expirable.LRU[string, struct{}]
	outputs  []eventSink
	rawHash  bool

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []eventWrite
	stopping bool

	wg  sync.WaitGroup
	log *slog.Logger
}

type eventWrite struct {
	event      Event
	rawPayload string
}

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
	Operation      Operation   `json:"operation,omitempty"`
	Decision       Decision    `json:"decision,omitempty"`
	Violations     []Violation `json:"violations,omitempty"`
	RawPayloadHash string      `json:"raw_payload_hash,omitempty"`
}

type Operation struct {
	CWD          string `json:"cwd,omitempty"`
	EffectiveCWD string `json:"effective_cwd,omitempty"`
	Command      string `json:"command,omitempty"`
	FilePath     string `json:"file_path,omitempty"`
}

type Decision struct {
	Kind         string   `json:"kind,omitempty"`
	CanBlock     bool     `json:"can_block,omitempty"`
	RulesChecked []string `json:"rules_checked,omitempty"`
	RulesMatched []string `json:"rules_matched,omitempty"`
}

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

func NewEventLogger(cfg *config.Config, log *slog.Logger) (*EventLogger, error) {
	if log == nil {
		log = slog.Default()
	}
	level := ""
	if cfg != nil {
		level = cfg.AuditLevel()
	}
	el := &EventLogger{
		minLevel: parseLevel(level),
		log:      log,
	}
	el.cond = sync.NewCond(&el.mu)
	el.dedup = expirable.NewLRU[string, struct{}](dedupCacheSize, nil, dedupTTL)

	if cfg == nil || cfg.AuditEnabled() {
		if cfg == nil || cfg.AuditJSONLEnabled() {
			eventsDir := config.DefaultAuditEventsDir()
			payloadsDir := config.DefaultAuditPayloadsDir()
			writeRaw := true
			if cfg != nil {
				eventsDir = cfg.AuditEventsDir()
				payloadsDir = cfg.AuditPayloadsDir()
				writeRaw = cfg.AuditWriteRawPayloads()
			}
			el.rawHash = writeRaw
			s, err := newJSONLEventSink(eventsDir, payloadsDir, writeRaw, log)
			if err != nil {
				return nil, err
			}
			el.outputs = append(el.outputs, s)
		}
		if cfg != nil && cfg.AuditSQLiteEnabled() {
			s, err := newSQLiteEventSink(cfg.AuditSQLitePath())
			if err != nil {
				return nil, err
			}
			el.outputs = append(el.outputs, s)
		}
	}

	el.wg.Add(1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				el.log.Error("audit worker panic recovered", slog.Any("err", recovered))
			}
			el.wg.Done()
		}()
		el.worker()
	}()
	return el, nil
}

func (el *EventLogger) Log(system, sessionID, eventName, level, msg string, attrs Attrs) {
	if el == nil || !el.shouldLog(level) {
		return
	}

	event := normalizeEvent(system, sessionID, eventName, level, msg, attrs)
	fingerprint := dedupFingerprint(event, attrs)
	if _, seen := el.dedup.Get(fingerprint); seen {
		el.log.Debug("audit event dedup drop",
			"system", event.System,
			"session_id", event.SessionID,
			"event", event.EventName,
			"msg", msg,
		)
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

	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		return
	}
	el.queue = append(el.queue, eventWrite{event: event, rawPayload: rawPayload})
	el.cond.Signal()
	el.mu.Unlock()
}

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
			RulesChecked: stringSliceAttr(attrs, "rules_checked"),
			RulesMatched: stringSliceAttr(attrs, "blocking_rules"),
		},
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
	switch msg {
	case "hook.allowed":
		return "allow"
	case "hook.blocked":
		return "block"
	case "hook.audit_violation":
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
		out = append(out, Violation{Rule: rule, Mode: mode, Message: message})
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
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
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

type jsonlEventSink struct {
	eventsDir        string
	payloadsDir      string
	writeRawPayloads bool
	cache            *lru.Cache[string, *os.File]
}

func newJSONLEventSink(eventsDir, payloadsDir string, writeRawPayloads bool, log *slog.Logger) (*jsonlEventSink, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(eventsDir, 0o755); err != nil {
		log.Error("create audit events dir failed",
			slog.String("events_dir", eventsDir),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("create audit events dir %s: %w", eventsDir, err)
	}
	if writeRawPayloads {
		if err := os.MkdirAll(payloadsDir, 0o755); err != nil {
			log.Error("create audit payloads dir failed",
				slog.String("payloads_dir", payloadsDir),
				slog.Any("err", err),
			)
			return nil, fmt.Errorf("create audit payloads dir %s: %w", payloadsDir, err)
		}
	}
	cache, err := lru.NewWithEvict[string, *os.File](eventLogCacheSize, func(_ string, f *os.File) {
		_ = f.Close()
	})
	if err != nil {
		log.Error("create audit event lru failed",
			slog.Int("cache_size", eventLogCacheSize),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("create audit event lru: %w", err)
	}
	return &jsonlEventSink{eventsDir: eventsDir, payloadsDir: payloadsDir, writeRawPayloads: writeRawPayloads, cache: cache}, nil
}

func (s *jsonlEventSink) Write(event Event, rawPayload string) error {
	if s.writeRawPayloads && rawPayload != "" {
		hash, err := s.writePayload(rawPayload)
		if err != nil {
			return err
		}
		event.RawPayloadHash = hash
	}

	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := s.fileFor(event.Time)
	if err != nil {
		return err
	}
	_, err = f.Write(line)
	return err
}

func (s *jsonlEventSink) writePayload(rawPayload string) (string, error) {
	hash := strings.TrimPrefix(payloadHash(rawPayload), "sha256:")
	dir := filepath.Join(s.payloadsDir, "sha256", hash[:2], hash[2:4])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hash+".json")
	if _, err := os.Stat(path); err == nil {
		return "sha256:" + hash, nil
	}
	return "sha256:" + hash, os.WriteFile(path, []byte(rawPayload), 0o600)
}

func payloadHash(rawPayload string) string {
	sum := sha256.Sum256([]byte(rawPayload))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *jsonlEventSink) fileFor(ts string) (*os.File, error) {
	day := ts[:10]
	if f, ok := s.cache.Get(day); ok {
		return f, nil
	}
	dir := filepath.Join(s.eventsDir, day[:4], day[5:7], day[8:10])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "events.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s.cache.Add(day, f)
	return f, nil
}

func (s *jsonlEventSink) Close() error {
	if s == nil || s.cache == nil {
		return nil
	}
	s.cache.Purge()
	return nil
}

type sqliteEventSink struct {
	db *sql.DB
}

func newSQLiteEventSink(path string) (*sqliteEventSink, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
	s := &sqliteEventSink{db: db}
	if err := s.init(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *sqliteEventSink) init() error {
	stmts := []string{
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
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqliteEventSink) Write(event Event, _ string) error {
	checked, _ := json.Marshal(event.Decision.RulesChecked)
	matched, _ := json.Marshal(event.Decision.RulesMatched)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(`insert or ignore into events values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.EventID, event.SchemaVersion, event.Time, event.Level, event.Message, event.System,
		event.SessionID, event.TurnID, event.EventName, event.ToolUseID, event.ToolName, event.RawPayloadHash,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert or ignore into operations values (?, ?, ?, ?, ?)`,
		event.EventID, event.Operation.CWD, event.Operation.EffectiveCWD, event.Operation.Command, event.Operation.FilePath,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert or ignore into decisions values (?, ?, ?, ?, ?)`,
		event.EventID, event.Decision.Kind, boolInt(event.Decision.CanBlock), string(checked), string(matched),
	); err != nil {
		return err
	}
	for _, v := range event.Violations {
		if _, err := tx.Exec(`insert into violations (event_id, rule, mode, field_path, file_path, start, end, message) values (?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventID, v.Rule, v.Mode, v.FieldPath, v.FilePath, v.Start, v.End, v.Message,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (s *sqliteEventSink) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

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
		out[key] = NewIntValue(int64(value.Uint64()))
	case slog.KindFloat64:
		out[key] = NewFloatValue(value.Float64())
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
