// Package intake persists append-first hook intake records and deferred replay
// state in SQLite so audit can be rebuilt from durable event ids.
package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
)

const schemaVersion = 1

const sqliteBusyTimeoutMS = 5000

// DeferredState tracks whether an intake event is still waiting for deferred
// replay or has already been processed.
type DeferredState string

// Deferred replay state variants.
const (
	// DeferredStateNone means no deferred replay state has been recorded yet.
	DeferredStateNone DeferredState = "none"
	// DeferredStatePending means deferred replay still needs to run.
	DeferredStatePending DeferredState = "pending"
	// DeferredStateComplete means deferred replay finished successfully.
	DeferredStateComplete DeferredState = "complete"
)

// ErrEventNotFound reports that the requested durable intake event id does not
// exist.
var ErrEventNotFound = errors.New("intake event not found")

// Operation captures the filesystem or shell context extracted from a hook
// payload at append time.
type Operation struct {
	CWD          string
	EffectiveCWD string
	Command      string
	FilePath     string
}

// Record is one durable hook intake event plus its deferred replay metadata.
type Record struct {
	EventID         string
	SchemaVersion   int
	RecordedAt      time.Time
	System          string
	SessionID       string
	TurnID          string
	EventName       string
	ToolName        string
	ToolUseID       string
	Operation       Operation
	RawPayload      []byte
	NormalizedJSON  json.RawMessage
	RawPayloadHash  string
	EnvFingerprint  map[string]string
	DeferredState   DeferredState
	PendingAt       *time.Time
	CompletedAt     *time.Time
	LastReplayAt    *time.Time
	DeferredReplays int
	Sequence        int64
}

// AppendResult reports the durable event id and whether a new row was
// inserted.
type AppendResult struct {
	ReceiptID int64
	EventID   string
	Inserted  bool
}

// Store owns the SQLite-backed durable intake tables.
type Store struct {
	db          *sql.DB
	log         *slog.Logger
	evaluations *evaluation.Store
}

var intakeNow = time.Now

// DefaultSQLitePath returns the default SQLite path used for durable intake.
func DefaultSQLitePath() string {
	return config.DefaultAuditSQLitePath()
}

// OpenSQLite opens the durable intake store, creating tables on demand.
func OpenSQLite(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = DefaultSQLitePath()
	}
	if log == nil {
		log = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, wrapLoggedError(ctx, log, "create intake sqlite dir", err)
	}
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, wrapLoggedError(ctx, log, "open intake sqlite db", err)
	}
	configureSQLite(db)
	store := &Store{
		db:  db,
		log: log,
	}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.evaluations, err = evaluation.NewStore(ctx, db)
	if err != nil {
		_ = db.Close()
		return nil, wrapLoggedError(ctx, log, "init evaluation store", err)
	}
	return store, nil
}

func configureSQLite(db *sql.DB) {
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
}

// Handle returns the underlying SQLite handle so a co-located writer, namely the
// audit event sink, can share this store's single connection pool. Sharing one
// pool serializes all writes to audit.db and avoids the cross-pool SQLITE_BUSY
// contention that two independent pools hit during the startup intake replay.
func (s *Store) Handle() *sql.DB {
	if s == nil {
		return nil
	}
	return s.db
}

// Evaluations returns the typed evaluation store that shares this connection.
func (s *Store) Evaluations() *evaluation.Store {
	if s == nil {
		return nil
	}
	return s.evaluations
}

// Close closes the underlying SQLite handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return wrapLoggedError(context.Background(), s.log, "close intake sqlite db", err)
	}
	return nil
}

// Append inserts one durable intake record, deduping by stable event id.
func (s *Store) Append(ctx context.Context, record Record) (AppendResult, error) {
	normalizedJSON, err := normalizeJSON(record.NormalizedJSON)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "normalize intake payload", err)
	}
	record.NormalizedJSON = normalizedJSON
	record = normalizeRecord(record)
	if record.EventID == "" {
		record.EventID = stableEventID(record)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "begin intake append tx", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	result, err := tx.ExecContext(ctx, `
		insert or ignore into intake_events (
			event_id,
			schema_version,
			recorded_at,
			system,
			session_id,
			turn_id,
			event_name,
			tool_name,
			tool_use_id,
			cwd,
			effective_cwd,
			command,
			file_path,
			raw_payload,
			raw_payload_hash,
			normalized_json,
			env_fingerprint_json
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.EventID,
		record.SchemaVersion,
		record.RecordedAt.UTC().Format(time.RFC3339Nano),
		record.System,
		record.SessionID,
		record.TurnID,
		record.EventName,
		record.ToolName,
		record.ToolUseID,
		record.Operation.CWD,
		record.Operation.EffectiveCWD,
		record.Operation.Command,
		record.Operation.FilePath,
		record.RawPayload,
		record.RawPayloadHash,
		string(record.NormalizedJSON),
		mustMarshalEnvFingerprint(record.EnvFingerprint),
	)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "insert intake event", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "read intake append rows", err)
	}
	receiptResult, err := tx.ExecContext(ctx, `
		insert into intake_receipts (event_id, received_at)
		values (?, ?)
	`, record.EventID, intakeNow().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "insert intake receipt", err)
	}
	receiptID, err := receiptResult.LastInsertId()
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "read intake receipt id", err)
	}
	if err := tx.Commit(); err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "commit intake append tx", err)
	}
	return AppendResult{
		ReceiptID: receiptID,
		EventID:   record.EventID,
		Inserted:  rowsAffected == 1,
	}, nil
}

// MarkDeferredPending marks an intake record ready for deferred replay.
func (s *Store) MarkDeferredPending(ctx context.Context, eventID string) error {
	now := intakeNow().UTC().Format(time.RFC3339Nano)
	return s.withExistingEvent(ctx, eventID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			insert into intake_deferred (
				event_id,
				state,
				pending_at,
				completed_at,
				last_replay_at,
				replay_count
			) values (?, ?, ?,
				null,
				cast(null as text),
				0)
			on conflict(event_id) do update set
				state = excluded.state,
				pending_at = coalesce(intake_deferred.pending_at, excluded.pending_at),
				completed_at = null
		`, eventID, DeferredStatePending, now)
		if err != nil {
			return wrapLoggedError(ctx, s.log, "mark intake deferred pending", err)
		}
		return nil
	})
}

// MarkDeferredComplete marks an intake record as fully replayed.
func (s *Store) MarkDeferredComplete(ctx context.Context, eventID string) error {
	now := intakeNow().UTC().Format(time.RFC3339Nano)
	return s.withExistingEvent(ctx, eventID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			insert into intake_deferred (
				event_id,
				state,
				pending_at,
				completed_at,
				last_replay_at,
				replay_count
			) values (?, ?,
				null,
				?,
				null,
				0)
			on conflict(event_id) do update set
				state = excluded.state,
				completed_at = excluded.completed_at
		`, eventID, DeferredStateComplete, now)
		if err != nil {
			return wrapLoggedError(ctx, s.log, "mark intake deferred complete", err)
		}
		return nil
	})
}

// ListDeferredPending returns pending intake records in append order.
func (s *Store) ListDeferredPending(ctx context.Context, limit int) ([]Record, error) {
	query := `
		select
			e.seq,
			e.event_id,
			e.schema_version,
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
			e.raw_payload,
			e.raw_payload_hash,
			e.normalized_json,
			e.env_fingerprint_json,
			d.state,
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			d.replay_count
		from intake_events e
		join intake_deferred d on d.event_id = e.event_id
		where d.state = ?
		order by e.seq asc
	`
	var rows *sql.Rows
	var err error
	if limit > 0 {
		query += " limit ?"
		rows, err = s.db.QueryContext(ctx, query, DeferredStatePending, limit)
	} else {
		rows, err = s.db.QueryContext(ctx, query, DeferredStatePending)
	}
	if err != nil {
		return nil, wrapLoggedError(ctx, s.log, "query pending intake events", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	records := make([]Record, 0)
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapLoggedError(ctx, s.log, "iterate pending intake events", err)
	}
	return records, nil
}

// Get loads one durable intake record by event id.
func (s *Store) Get(ctx context.Context, eventID string) (Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			e.seq,
			e.event_id,
			e.schema_version,
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
			e.raw_payload,
			e.raw_payload_hash,
			e.normalized_json,
			e.env_fingerprint_json,
			coalesce(d.state, ?),
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			coalesce(d.replay_count, 0)
		from intake_events e
		left join intake_deferred d on d.event_id = e.event_id
		where e.event_id = ?
	`, DeferredStateNone, eventID)
	if err != nil {
		return Record{}, wrapLoggedError(ctx, s.log, "query intake record", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	if !rows.Next() {
		return Record{}, ErrEventNotFound
	}
	record, err := scanRecord(rows)
	if err != nil {
		return Record{}, err
	}
	if err := rows.Err(); err != nil {
		return Record{}, wrapLoggedError(ctx, s.log, "iterate intake record", err)
	}
	return record, nil
}

// ReplayDeferredPending walks pending records in order and records replay
// metadata before invoking replay.
func (s *Store) ReplayDeferredPending(ctx context.Context, limit int, replay func(Record) error) error {
	records, err := s.ListDeferredPending(ctx, limit)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := s.noteReplay(ctx, record.EventID); err != nil {
			return err
		}
		refreshed, err := s.pendingRecord(ctx, record.EventID)
		if err != nil {
			return err
		}
		if err := replay(refreshed); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) init(ctx context.Context) error {
	stmts := []string{
		fmt.Sprintf(`pragma busy_timeout = %d`, sqliteBusyTimeoutMS),
		`pragma journal_mode = wal`,
		`pragma foreign_keys = on`,
		`create table if not exists intake_events (
			seq integer primary key autoincrement,
			event_id text not null unique,
			schema_version integer not null,
			recorded_at text not null,
			system text not null,
			session_id text not null,
			turn_id text not null,
			event_name text not null,
			tool_name text not null,
			tool_use_id text not null,
			cwd text not null,
			effective_cwd text not null,
			command text not null,
			file_path text not null,
			raw_payload blob not null,
			raw_payload_hash text not null,
			normalized_json text not null,
			env_fingerprint_json text not null default '{}'
		)`,
		`create table if not exists intake_deferred (
			event_id text primary key,
			state text not null,
			pending_at text,
			completed_at text,
			last_replay_at text,
			replay_count integer not null default 0,
			foreign key(event_id) references intake_events(event_id) on delete cascade,
			check(state in ('none', 'pending', 'complete'))
		)`,
		`create table if not exists intake_receipts (
			receipt_id integer primary key autoincrement,
			event_id text not null,
			received_at text not null,
			foreign key(event_id) references intake_events(event_id) on delete cascade
		)`,
		`create index if not exists intake_events_recorded_at_idx on intake_events(recorded_at)`,
		`create index if not exists intake_events_session_recorded_at_idx on intake_events(session_id, recorded_at)`,
		`create index if not exists intake_events_system_recorded_at_idx on intake_events(system, recorded_at)`,
		`create index if not exists intake_deferred_state_idx on intake_deferred(state)`,
		`create index if not exists intake_receipts_event_id_idx on intake_receipts(event_id)`,
		`create index if not exists intake_receipts_received_at_idx on intake_receipts(received_at)`,
		// Per-slice indices so common group-by/filter queries use an index
		// instead of scanning the table. command, raw_payload, normalized_json,
		// and env_fingerprint_json are intentionally excluded: they are free
		// text, blobs, or JSON, and a LIKE '%...%' scan cannot use a b-tree.
		// command is searched through the FTS5 trigram index instead.
		`create index if not exists intake_events_event_name_idx on intake_events(event_name)`,
		`create index if not exists intake_events_tool_name_idx on intake_events(tool_name)`,
		`create index if not exists intake_events_tool_use_id_idx on intake_events(tool_use_id)`,
		`create index if not exists intake_events_turn_id_idx on intake_events(turn_id)`,
		`create index if not exists intake_events_cwd_idx on intake_events(cwd)`,
		`create index if not exists intake_events_effective_cwd_idx on intake_events(effective_cwd)`,
		`create index if not exists intake_events_file_path_idx on intake_events(file_path)`,
		`create index if not exists intake_events_raw_payload_hash_idx on intake_events(raw_payload_hash)`,
		`create index if not exists intake_events_schema_version_idx on intake_events(schema_version)`,
		`create index if not exists intake_events_effective_cwd_recorded_at_idx on intake_events(effective_cwd, recorded_at)`,
		`create index if not exists intake_events_tool_name_recorded_at_idx on intake_events(tool_name, recorded_at)`,
		`create index if not exists intake_events_event_name_recorded_at_idx on intake_events(event_name, recorded_at)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return wrapLoggedError(ctx, s.log, "init intake sqlite schema", err)
		}
	}
	if err := ensureIntakeSchemaMigrations(ctx, s.db); err != nil {
		return err
	}
	ensureCommandFTS(ctx, s.db, s.log)
	return nil
}

func (s *Store) withExistingEvent(ctx context.Context, eventID string, run func(tx *sql.Tx) error) error {
	if strings.TrimSpace(eventID) == "" {
		return ErrEventNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "begin intake state tx", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var exists int
	err = tx.QueryRowContext(ctx, `select 1 from intake_events where event_id = ?`, eventID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEventNotFound
	}
	if err != nil {
		return wrapLoggedError(ctx, s.log, "lookup intake event", err)
	}
	if err := run(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapLoggedError(ctx, s.log, "commit intake state tx", err)
	}
	return nil
}

func (s *Store) noteReplay(ctx context.Context, eventID string) error {
	now := intakeNow().UTC().Format(time.RFC3339Nano)
	return s.withExistingEvent(ctx, eventID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			update intake_deferred
			set last_replay_at = ?, replay_count = replay_count + 1
			where event_id = ? and state = ?
		`, now, eventID, DeferredStatePending)
		if err != nil {
			return wrapLoggedError(ctx, s.log, "update intake replay metadata", err)
		}
		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return wrapLoggedError(ctx, s.log, "read intake replay rows", err)
		}
		if rowsAffected == 0 {
			return ErrEventNotFound
		}
		return nil
	})
}

func (s *Store) pendingRecord(ctx context.Context, eventID string) (Record, error) {
	rows, err := s.db.QueryContext(ctx, `
		select
			e.seq,
			e.event_id,
			e.schema_version,
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
			e.raw_payload,
			e.raw_payload_hash,
			e.normalized_json,
			e.env_fingerprint_json,
			d.state,
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			d.replay_count
		from intake_events e
		join intake_deferred d on d.event_id = e.event_id
		where e.event_id = ? and d.state = ?
	`, eventID, DeferredStatePending)
	if err != nil {
		return Record{}, wrapLoggedError(ctx, s.log, "query refreshed intake record", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	if !rows.Next() {
		return Record{}, ErrEventNotFound
	}
	record, err := scanRecord(rows)
	if err != nil {
		return Record{}, err
	}
	if err := rows.Err(); err != nil {
		return Record{}, wrapLoggedError(ctx, s.log, "iterate refreshed intake record", err)
	}
	return record, nil
}

func scanRecord(rows *sql.Rows) (Record, error) {
	var (
		recordedAt     string
		normalized     string
		envFingerprint string
		state          string
		pendingAt      sql.NullString
		completedAt    sql.NullString
		lastReplayAt   sql.NullString
		rawPayload     []byte
		rawPayloadHash string
		record         Record
	)
	err := rows.Scan(
		&record.Sequence,
		&record.EventID,
		&record.SchemaVersion,
		&recordedAt,
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
		&rawPayload,
		&rawPayloadHash,
		&normalized,
		&envFingerprint,
		&state,
		&pendingAt,
		&completedAt,
		&lastReplayAt,
		&record.DeferredReplays,
	)
	if err != nil {
		return Record{}, wrapError("scan intake record", err)
	}
	record.RecordedAt, err = time.Parse(time.RFC3339Nano, recordedAt)
	if err != nil {
		return Record{}, wrapError("parse intake recorded_at", err)
	}
	record.NormalizedJSON = json.RawMessage(normalized)
	record.RawPayload = append([]byte(nil), rawPayload...)
	record.RawPayloadHash = rawPayloadHash
	record.EnvFingerprint, err = unmarshalEnvFingerprint(envFingerprint)
	if err != nil {
		return Record{}, err
	}
	record.DeferredState = DeferredState(state)
	record.PendingAt, err = parseOptionalTime(pendingAt)
	if err != nil {
		return Record{}, err
	}
	record.CompletedAt, err = parseOptionalTime(completedAt)
	if err != nil {
		return Record{}, err
	}
	record.LastReplayAt, err = parseOptionalTime(lastReplayAt)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func parseOptionalTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, wrapError("parse intake optional timestamp", err)
	}
	return &parsed, nil
}

func normalizeRecord(record Record) Record {
	if record.SchemaVersion == 0 {
		record.SchemaVersion = schemaVersion
	}
	if record.RecordedAt.IsZero() {
		record.RecordedAt = intakeNow().UTC()
	}
	record.System = strings.TrimSpace(record.System)
	record.SessionID = strings.TrimSpace(record.SessionID)
	record.TurnID = strings.TrimSpace(record.TurnID)
	record.EventName = strings.TrimSpace(record.EventName)
	record.ToolName = strings.TrimSpace(record.ToolName)
	record.ToolUseID = strings.TrimSpace(record.ToolUseID)
	record.Operation.CWD = strings.TrimSpace(record.Operation.CWD)
	record.Operation.EffectiveCWD = strings.TrimSpace(record.Operation.EffectiveCWD)
	record.Operation.Command = strings.TrimSpace(record.Operation.Command)
	record.Operation.FilePath = strings.TrimSpace(record.Operation.FilePath)
	record.RawPayload = append([]byte(nil), record.RawPayload...)
	record.EnvFingerprint = cloneEnvFingerprint(record.EnvFingerprint)
	if record.RawPayloadHash == "" {
		record.RawPayloadHash = payloadHash(record.RawPayload)
	}
	return record
}

func normalizeJSON(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	var normalized bytes.Buffer
	if err := json.Compact(&normalized, raw); err != nil {
		return nil, wrapError("decode intake normalized json", err)
	}
	return json.RawMessage(normalized.Bytes()), nil
}

func stableEventID(record Record) string {
	hash := sha256.New()
	writeHashPart(hash, record.System)
	writeHashPart(hash, record.SessionID)
	writeHashPart(hash, record.TurnID)
	writeHashPart(hash, record.EventName)
	writeHashPart(hash, record.ToolName)
	writeHashPart(hash, record.ToolUseID)
	writeHashPart(hash, record.Operation.CWD)
	writeHashPart(hash, record.Operation.EffectiveCWD)
	writeHashPart(hash, record.Operation.Command)
	writeHashPart(hash, record.Operation.FilePath)
	writeHashPart(hash, string(record.NormalizedJSON))
	writeHashPart(hash, mustMarshalEnvFingerprint(record.EnvFingerprint))
	_, _ = hash.Write(record.RawPayload)
	return "intake_" + hex.EncodeToString(hash.Sum(nil))
}

func writeHashPart(hash hash.Hash, value string) {
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}

func payloadHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func cloneEnvFingerprint(envFingerprint map[string]string) map[string]string {
	if len(envFingerprint) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(envFingerprint))
	maps.Copy(cloned, envFingerprint)
	return cloned
}

func mustMarshalEnvFingerprint(envFingerprint map[string]string) string {
	normalized := cloneEnvFingerprint(envFingerprint)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func unmarshalEnvFingerprint(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]string{}, nil
	}
	var envFingerprint map[string]string
	if err := json.Unmarshal([]byte(raw), &envFingerprint); err != nil {
		return nil, wrapError("decode intake env fingerprint", err)
	}
	return cloneEnvFingerprint(envFingerprint), nil
}

func ensureIntakeSchemaMigrations(ctx context.Context, db *sql.DB) error {
	if err := ensureIntakeEventColumn(ctx, db, "env_fingerprint_json", "text not null default '{}'"); err != nil {
		return err
	}
	if err := ensureIntakeEventColumn(ctx, db, "hot_eval_latency_us", "integer"); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `create index if not exists intake_events_hot_eval_latency_us_idx on intake_events(hot_eval_latency_us)`); err != nil {
		return wrapError("create intake_events hot_eval_latency_us index", err)
	}
	return nil
}

// commandFTSTriggers keeps the external-content FTS5 index in sync with
// intake_events. The update trigger is scoped to OF command so a latency-only
// write-back never churns the index.
var commandFTSTriggers = []string{
	`create trigger if not exists intake_events_ai after insert on intake_events begin
		insert into intake_command_fts(rowid, command) values (new.seq, new.command);
	end`,
	`create trigger if not exists intake_events_ad after delete on intake_events begin
		insert into intake_command_fts(intake_command_fts, rowid, command) values('delete', old.seq, old.command);
	end`,
	`create trigger if not exists intake_events_au after update of command on intake_events begin
		insert into intake_command_fts(intake_command_fts, rowid, command) values('delete', old.seq, old.command);
		insert into intake_command_fts(rowid, command) values (new.seq, new.command);
	end`,
}

// ensureCommandFTS creates the FTS5 trigram index over intake_events.command and
// its sync triggers, then backfills the pre-existing rows exactly once. It is
// best-effort: a binary built without the sqlite_fts5 tag cannot load the fts5
// module, so creation fails and the index is skipped rather than breaking
// intake. The rebuild runs only when the FTS table did not exist before this
// call, because for an external-content FTS5 table "select count(*) from
// intake_command_fts" reads the content table (always the full row count) and
// cannot reveal an empty index.
func ensureCommandFTS(ctx context.Context, db *sql.DB, log *slog.Logger) {
	alreadyExisted := commandFTSExists(ctx, db)

	_, err := db.ExecContext(ctx, `create virtual table if not exists intake_command_fts using fts5(
		command,
		content='intake_events',
		content_rowid='seq',
		tokenize='trigram'
	)`)
	if err != nil {
		if log != nil {
			log.WarnContext(ctx, "fts5 command index unavailable; substring search will scan", "err", err)
		}
		return
	}
	for _, stmt := range commandFTSTriggers {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			if log != nil {
				log.WarnContext(ctx, "create intake command fts trigger failed", "err", err)
			}
			return
		}
	}
	if alreadyExisted {
		return
	}
	if _, err := db.ExecContext(ctx, `insert into intake_command_fts(intake_command_fts) values('rebuild')`); err != nil {
		if log != nil {
			log.WarnContext(ctx, "backfill intake command fts failed", "err", err)
		}
	}
}

// commandFTSExists reports whether the FTS table is already present, so the
// one-time backfill runs only on first creation.
func commandFTSExists(ctx context.Context, db *sql.DB) bool {
	var name string
	err := db.QueryRowContext(ctx, `select name from sqlite_master where type = 'table' and name = 'intake_command_fts'`).Scan(&name)
	return err == nil
}

// UpdateHotEvalLatency records the synchronous hot-path evaluation latency for a
// durable event. It targets only hot_eval_latency_us, so the FTS update trigger
// (scoped to the command column) does not fire.
func (s *Store) UpdateHotEvalLatency(ctx context.Context, eventID string, latencyMicros int64) error {
	if strings.TrimSpace(eventID) == "" {
		return ErrEventNotFound
	}
	_, err := s.db.ExecContext(ctx, `update intake_events set hot_eval_latency_us = ? where event_id = ?`, latencyMicros, eventID)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "update intake hot_eval_latency_us", err)
	}
	return nil
}

func ensureIntakeEventColumn(ctx context.Context, db *sql.DB, columnName string, definition string) error {
	rows, err := db.QueryContext(ctx, `pragma table_info(intake_events)`)
	if err != nil {
		return wrapError("query intake_events schema", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &primaryKey); err != nil {
			return wrapError("scan intake_events schema", err)
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return wrapError("iterate intake_events schema", err)
	}

	statement := fmt.Sprintf("alter table intake_events add column %s %s", columnName, definition)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return wrapError("add intake_events."+columnName, err)
	}
	return nil
}

func wrapLoggedError(ctx context.Context, log *slog.Logger, message string, err error) error {
	if err == nil {
		return nil
	}
	if log != nil {
		log.WarnContext(ctx, message+" failed", "err", err)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
