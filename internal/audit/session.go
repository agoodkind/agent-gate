package audit

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	expirable "github.com/hashicorp/golang-lru/v2/expirable"
	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
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
// and a concrete SQLite writer.
type EventLogger struct {
	minLevel slog.Level
	dedup    *expirable.LRU[string, struct{}]
	writer   *sqliteWriter
	rawHash  bool
	enabled  bool

	mu              sync.Mutex
	cond            *sync.Cond
	queue           []eventWrite
	queuedBytes     int
	batchMaxItems   int
	batchMaxBytes   int
	queueMaxBytes   int
	limit           int
	dropped         uint64
	dropLogInterval time.Duration
	lastDrop        time.Time
	stopping        bool
	closeDone       chan struct{}
	closeErr        error
	workerCancel    context.CancelFunc

	wg       sync.WaitGroup
	log      *slog.Logger
	sharedDB *sql.DB
}

type eventWrite struct {
	event Event
	bytes int
}

// LoggerOptions tunes queue behavior for tests and high-throughput daemon use.
// SharedDB, when set, makes the SQLite sink write through an existing handle
// (the intake store's) instead of opening its own, so audit and intake writes
// share one serialized connection pool.
type LoggerOptions struct {
	QueueLimit    int
	BatchMaxItems int
	BatchMaxBytes int
	QueueMaxBytes int
	SharedDB      *sql.DB
}

// Event is one normalized audit record. It is the canonical schema written
// to SQLite.
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

// NormalizedEntry is an immutable audit write that can be retried without
// changing its timestamp, event identity, or normalized fields.
type NormalizedEntry struct {
	Event       Event  `json:"event"`
	Fingerprint string `json:"fingerprint"`
}

func emptyNormalizedEntry() NormalizedEntry {
	return NormalizedEntry{
		Event: Event{
			EventID: "", SchemaVersion: 0, Time: "", Level: "", Message: "",
			System: "", SessionID: "", TurnID: "", EventName: "", ToolUseID: "",
			ToolName: "", Operation: Operation{
				CWD: "", EffectiveCWD: "", Command: "", FilePath: "",
			},
			Decision: Decision{
				Kind: "", CanBlock: false, RulesChecked: nil, RulesMatched: nil,
			},
			Violations: nil, RawPayloadHash: "",
		},
		Fingerprint: "",
	}
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
		queueLimit = cfg.AuditQueueLimit()
	}
	el := new(EventLogger)
	workerContext, workerCancel := context.WithCancel(context.WithoutCancel(ctx))
	el.workerCancel = workerCancel
	el.closeDone = make(chan struct{})
	el.minLevel = parseLevel(level)
	el.enabled = true
	el.limit = queueLimit
	el.batchMaxItems = options.BatchMaxItems
	if el.batchMaxItems <= 0 {
		el.batchMaxItems = 64
	}
	el.batchMaxBytes = options.BatchMaxBytes
	if el.batchMaxBytes <= 0 {
		el.batchMaxBytes = 1024 * 1024
	}
	el.queueMaxBytes = options.QueueMaxBytes
	if el.queueMaxBytes <= 0 {
		el.queueMaxBytes = 16 * 1024 * 1024
	}
	el.log = log
	el.sharedDB = options.SharedDB
	el.cond = sync.NewCond(&el.mu)
	el.dropLogInterval = cfg.AuditDropLogInterval()
	el.dedup = expirable.NewLRU[string, struct{}](cfg.AuditDedupCacheSize(), nil, cfg.AuditDedupTTL())

	if cfg != nil && !cfg.AuditEnabled() {
		el.enabled = false
	}
	if el.enabled {
		if err := el.configureOutputs(ctx, cfg); err != nil {
			workerCancel()
			return nil, err
		}
	}
	if el.writer == nil {
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
			// Accepted asynchronous events belong to the logger until Close drains them.
			el.worker(workerContext)
		}()
	}
	return el, nil
}

// Enabled reports whether the logger has at least one active output.
func (el *EventLogger) Enabled() bool {
	return el != nil && el.enabled
}

func (el *EventLogger) configureOutputs(ctx context.Context, cfg *config.Config) error {
	el.rawHash = true
	path := config.DefaultAuditSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	database := el.sharedDB
	ownsDB := false
	if database == nil {
		var err error
		database, err = auditstorage.OpenWriter(ctx, path)
		if err != nil {
			return storageError("open audit writer", err)
		}
		ownsDB = true
	}
	el.writer = &sqliteWriter{db: database, ownsDB: ownsDB}
	return nil
}

// Log enqueues a normalized audit event for asynchronous write to all
// configured outputs. Calls return without waiting for I/O; the call is
// also a no-op when the receiver is nil or the level is filtered out.
func (el *EventLogger) Log(system, sessionID, eventName, level, msg string, attrs Attrs) {
	if el == nil || !el.enabled || !el.shouldLog(level) {
		return
	}
	entry := el.Normalize(system, sessionID, eventName, level, msg, attrs)
	encoded, err := json.Marshal(entry.Event)
	if err != nil || len(encoded) > el.batchMaxBytes {
		el.recordDrop(system, sessionID, eventName, msg)
		return
	}
	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		return
	}
	if len(el.queue) >= el.limit || len(encoded) > el.queueMaxBytes-el.queuedBytes {
		el.mu.Unlock()
		el.recordDrop(system, sessionID, eventName, msg)
		return
	}
	if _, seen := el.dedup.Get(entry.Fingerprint); seen {
		el.mu.Unlock()
		return
	}
	el.dedup.Add(entry.Fingerprint, struct{}{})
	el.queue = append(el.queue, eventWrite{event: entry.Event, bytes: len(encoded)})
	el.queuedBytes += len(encoded)
	el.cond.Signal()
	el.mu.Unlock()
}

// LogDurable writes a normalized audit event to every configured output before
// returning. Failed writes remain eligible for a later retry.
func (el *EventLogger) LogDurable(
	ctx context.Context,
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	attrs Attrs,
) error {
	if el == nil || !el.enabled || !el.shouldLog(level) {
		return nil
	}
	entry := el.Normalize(system, sessionID, eventName, level, msg, attrs)
	return el.LogNormalizedDurable(ctx, entry)
}

// Normalize captures the exact normalized audit entry used by durable writes.
func (el *EventLogger) Normalize(
	system string,
	sessionID string,
	eventName string,
	level string,
	msg string,
	attrs Attrs,
) NormalizedEntry {
	if el == nil || !el.enabled || !el.shouldLog(level) {
		return emptyNormalizedEntry()
	}
	event := normalizeEvent(system, sessionID, eventName, level, msg, attrs)
	fingerprint := dedupFingerprint(event, attrs)
	rawPayload := ""
	if value, ok := attrs["raw_payload"]; ok {
		rawPayload = value.String()
	}
	if rawPayload != "" && el.rawHash {
		event.RawPayloadHash = payloadHash(rawPayload)
	}
	event.EventID = "evt_" + fingerprint[:32]
	return NormalizedEntry{
		Event: event, Fingerprint: fingerprint,
	}
}

// LogNormalizedDurable writes a previously normalized entry without changing
// its identity or timestamp.
func (el *EventLogger) LogNormalizedDurable(
	ctx context.Context,
	entry NormalizedEntry,
) error {
	if el == nil || !el.enabled {
		return nil
	}
	encoded, err := json.Marshal(entry.Event)
	if err != nil {
		return storageError("encode audit event", err)
	}
	if len(encoded) > el.batchMaxBytes {
		return fmt.Errorf("audit event exceeds batch byte limit %d", el.batchMaxBytes)
	}
	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		return fmt.Errorf("audit logger is stopping")
	}
	if _, seen := el.dedup.Get(entry.Fingerprint); seen {
		el.mu.Unlock()
		return nil
	}
	el.wg.Add(1)
	el.mu.Unlock()
	defer el.wg.Done()
	if err := WriteEvents(ctx, el.writer.db, []Event{entry.Event}); err != nil {
		return err
	}
	el.mu.Lock()
	el.dedup.Add(entry.Fingerprint, struct{}{})
	el.mu.Unlock()
	return nil
}

var (
	_ DurableSink           = (*LocalSink)(nil)
	_ ReplayableDurableSink = (*LocalSink)(nil)
)

func (el *EventLogger) recordDrop(system, sessionID, eventName, msg string) {
	now := auditNow()
	el.mu.Lock()
	el.dropped++
	dropped := el.dropped
	if !el.lastDrop.IsZero() && now.Sub(el.lastDrop) < el.dropLogInterval {
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
	return el.CloseContext(context.Background())
}

// CloseContext drains accepted events until the shutdown deadline, then cancels writes.
// It joins the writer before returning so borrowed databases can close safely.
func (el *EventLogger) CloseContext(ctx context.Context) error {
	if el == nil {
		return nil
	}
	stopCancellation := context.AfterFunc(ctx, el.workerCancel)
	defer stopCancellation()
	el.mu.Lock()
	if el.stopping {
		el.mu.Unlock()
		<-el.closeDone
		return el.closeErr
	}
	el.stopping = true
	el.cond.Broadcast()
	el.mu.Unlock()

	el.wg.Wait()
	el.workerCancel()
	if el.writer != nil {
		el.closeErr = el.writer.Close()
	}
	el.closeErr = errors.Join(el.closeErr, ctx.Err())
	close(el.closeDone)
	return el.closeErr
}

func (el *EventLogger) worker(ctx context.Context) {
	for {
		el.mu.Lock()
		for len(el.queue) == 0 && !el.stopping {
			el.cond.Wait()
		}
		if len(el.queue) == 0 && el.stopping {
			el.mu.Unlock()
			return
		}
		count := 0
		size := 0
		events := make([]Event, 0, el.batchMaxItems)
		for count < len(el.queue) && count < el.batchMaxItems {
			next := el.queue[count]
			if size+next.bytes > el.batchMaxBytes {
				break
			}
			events = append(events, next.event)
			size += next.bytes
			count++
		}
		clear(el.queue[:count])
		el.queue = el.queue[count:]
		el.queuedBytes -= size
		el.mu.Unlock()
		if err := WriteEvents(ctx, el.writer.db, events); err != nil {
			el.log.Warn("audit batch write failed", "events", len(events), "err", err)
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
