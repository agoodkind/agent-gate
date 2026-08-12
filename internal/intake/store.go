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
	"hash"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/evaluation"
)

const schemaVersion = 1

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

// ErrReceiptEventMismatch reports that a receipt belongs to another event.
var ErrReceiptEventMismatch = errors.New("intake receipt event mismatch")

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
	ReceiptID          int64
	ReceivedAt         time.Time
	EventID            string
	SchemaVersion      int
	RecordedAt         time.Time
	System             string
	SessionID          string
	TurnID             string
	EventName          string
	ToolName           string
	ToolUseID          string
	Operation          Operation
	RawPayload         []byte
	NormalizedJSON     json.RawMessage
	ClassificationJSON json.RawMessage
	RawPayloadHash     string
	EnvFingerprint     map[string]string
	DeferredState      DeferredState
	PendingAt          *time.Time
	CompletedAt        *time.Time
	LastReplayAt       *time.Time
	DeferredReplays    int
	Sequence           int64
}

// DeferredClaim fences one deferred evaluation attempt to a single processor.
type DeferredClaim struct {
	ReceiptID int64
	EventID   string
	Owner     string
	Attempt   int
	ExpiresAt time.Time
}

// DeferredAuditClaim fences one outbox delivery attempt to one processor.
type DeferredAuditClaim struct {
	ReceiptID int64
	EventID   string
	Owner     string
	Attempt   int
	ExpiresAt time.Time
}

// DeferredAuditEntry is one ordered, immutable outbox entry.
type DeferredAuditEntry struct {
	Index int
	Entry audit.NormalizedEntry
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
	policy      config.AuditStoragePolicy
	evaluations *evaluation.Store
}

var intakeNow = time.Now

// DefaultSQLitePath returns the default SQLite path used for durable intake.
func DefaultSQLitePath() string {
	return config.DefaultAuditSQLitePath()
}

// SQLiteOptions configures one immutable intake store policy snapshot.
type SQLiteOptions struct {
	Path   string
	Policy config.AuditStoragePolicy
	Log    *slog.Logger
}

// OpenSQLite opens the durable intake store with the balanced compatibility policy.
func OpenSQLite(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	return openSQLite(ctx, SQLiteOptions{
		Path: path, Policy: balancedAuditStoragePolicy(), Log: log,
	})
}

// OpenSQLiteWithOptions opens the durable intake store with one policy snapshot.
func OpenSQLiteWithOptions(ctx context.Context, options SQLiteOptions) (*Store, error) {
	if options.Policy.Profile == "" {
		return OpenSQLite(ctx, options.Path, options.Log)
	}
	return openSQLite(ctx, options)
}

func openSQLite(ctx context.Context, options SQLiteOptions) (*Store, error) {
	if strings.TrimSpace(options.Path) == "" {
		options.Path = DefaultSQLitePath()
	}
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(options.Path), 0o755); err != nil {
		return nil, wrapLoggedError(ctx, options.Log, "create intake sqlite dir", err)
	}
	db, err := sql.Open("sqlite3", options.Path)
	if err != nil {
		return nil, wrapLoggedError(ctx, options.Log, "open intake sqlite db", err)
	}
	configureSQLite(db)
	store := &Store{
		db:          db,
		log:         options.Log,
		policy:      options.Policy,
		evaluations: nil,
	}
	if err := store.init(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	store.evaluations, err = evaluation.NewStoreWithPolicy(ctx, db, options.Policy)
	if err != nil {
		_ = db.Close()
		return nil, wrapLoggedError(ctx, options.Log, "init evaluation store", err)
	}
	return store, nil
}

func balancedAuditStoragePolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{
		Profile:                 config.AuditStorageProfileBalanced,
		MaintenanceInterval:     24 * time.Hour,
		MaxSizeBytes:            0,
		MaintenanceBatchRows:    1000,
		CompactAfterMaintenance: true,
		FullDetailRetention:     168 * time.Hour,
		SummaryRetention:        720 * time.Hour,
		Detail: config.AuditStorageDetailPolicy{
			WireInput: true, NormalizedInput: true, ProviderEvidence: true,
			EnvironmentEvidence: true, EvaluationContent: true,
		},
	}
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
	return s.append(ctx, record, false)
}

// AppendPending inserts one durable intake record ready for deferred evaluation.
func (s *Store) AppendPending(ctx context.Context, record Record) (AppendResult, error) {
	return s.append(ctx, record, true)
}

func (s *Store) append(
	ctx context.Context,
	record Record,
	deferredPending bool,
) (AppendResult, error) {
	normalizedJSON, err := normalizeJSON(record.NormalizedJSON)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "normalize intake payload", err)
	}
	record.NormalizedJSON = normalizedJSON
	classificationJSON, err := normalizeJSON(record.ClassificationJSON)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "normalize intake classification", err)
	}
	record.ClassificationJSON = classificationJSON
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
			insert into intake_events (
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
				raw_payload_hash
			) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			on conflict(event_id) do nothing
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
		record.RawPayloadHash,
	)
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "insert intake event", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "read intake append rows", err)
	}
	receivedAt := intakeNow().UTC()
	if err := s.insertDetail(ctx, tx, record, receivedAt); err != nil {
		return AppendResult{}, err
	}
	receiptResult, err := tx.ExecContext(ctx, `
			insert into intake_receipts (event_id, received_at)
			values (?, ?)
		`, record.EventID, receivedAt.Format(time.RFC3339Nano))
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "insert intake receipt", err)
	}
	receiptID, err := receiptResult.LastInsertId()
	if err != nil {
		return AppendResult{}, wrapLoggedError(ctx, s.log, "read intake receipt id", err)
	}
	if err := markDeferredPendingIfRequested(
		ctx, tx, deferredPending, receiptID, record.EventID, receivedAt,
	); err != nil {
		return AppendResult{}, err
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

func markDeferredPendingIfRequested(
	ctx context.Context,
	transaction *sql.Tx,
	deferredPending bool,
	receiptID int64,
	eventID string,
	receivedAt time.Time,
) error {
	if !deferredPending {
		return nil
	}
	return markDeferredPendingInTx(ctx, transaction, receiptID, eventID, receivedAt)
}

func (s *Store) insertDetail(
	ctx context.Context,
	transaction *sql.Tx,
	record Record,
	changedAt time.Time,
) error {
	details := []struct {
		class   auditstorage.DetailClass
		content []byte
	}{
		{class: auditstorage.DetailClassWireInput, content: record.RawPayload},
		{class: auditstorage.DetailClassNormalizedInput, content: record.NormalizedJSON},
		{class: auditstorage.DetailClassProviderEvidence, content: record.ClassificationJSON},
		{
			class:   auditstorage.DetailClassEnvironmentEvidence,
			content: []byte(mustMarshalEnvFingerprint(record.EnvFingerprint)),
		},
	}
	for _, detail := range details {
		if _, err := transaction.ExecContext(ctx, `
			insert into intake_event_details (event_id, detail_class, content)
			values (?, ?, ?)
			on conflict(event_id, detail_class) do nothing
		`, record.EventID, detail.class, detail.content); err != nil {
			return wrapLoggedError(ctx, s.log, "insert intake event detail", err)
		}
	}
	state := auditstorage.DetailStateAvailable
	if !s.policy.Detail.WireInput || !s.policy.Detail.NormalizedInput ||
		!s.policy.Detail.ProviderEvidence || !s.policy.Detail.EnvironmentEvidence {
		state = auditstorage.DetailStateProtected
	}
	if _, err := transaction.ExecContext(ctx, `
		insert into intake_event_detail_manifest (
			event_id, recorded_classes_json, available_classes_json, state, state_changed_at
		) values (?, ?, ?, ?, ?)
		on conflict(event_id) do update set
			recorded_classes_json = excluded.recorded_classes_json,
			available_classes_json = excluded.available_classes_json,
			state = excluded.state,
			state_changed_at = excluded.state_changed_at
	`, record.EventID, intakeDetailClassesJSON, intakeDetailClassesJSON, state,
		changedAt.Format(time.RFC3339Nano)); err != nil {
		return wrapLoggedError(ctx, s.log, "insert intake event detail manifest", err)
	}
	return nil
}

const intakeDetailClassesJSON = `["wire_input","normalized_input","provider_evidence","environment_evidence"]`

// MarkDeferredPending marks an intake record ready for deferred replay.
func (s *Store) MarkDeferredPending(ctx context.Context, eventID string, receiptID int64) error {
	return s.withExistingReceipt(ctx, eventID, receiptID, func(tx *sql.Tx, canonicalEventID string) error {
		return markDeferredPendingInTx(
			ctx, tx, receiptID, canonicalEventID, intakeNow().UTC(),
		)
	})
}

// MarkDeferredComplete marks an intake record as fully replayed.
func (s *Store) MarkDeferredComplete(ctx context.Context, receiptID int64) error {
	now := intakeNow().UTC().Format(time.RFC3339Nano)
	return s.withExistingReceipt(ctx, "", receiptID, func(tx *sql.Tx, eventID string) error {
		_, err := tx.ExecContext(ctx, `
			insert into intake_deferred (
				receipt_id,
				event_id,
				state,
				pending_at,
				completed_at,
				last_replay_at,
				replay_count
			) values (?, ?, ?,
				null,
				?,
				null,
				0)
			on conflict(receipt_id) do update set
				state = excluded.state,
				completed_at = excluded.completed_at
		`, receiptID, eventID, DeferredStateComplete, now)
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
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'wire_input'),
				e.raw_payload_hash,
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'normalized_input'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'provider_evidence'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'environment_evidence'),
			r.receipt_id,
			r.received_at,
			d.state,
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			coalesce(d.replay_count, 0)
		from intake_events e
		join intake_deferred d on d.event_id = e.event_id
		join intake_receipts r on r.receipt_id = d.receipt_id
		where d.state = ?
		order by r.receipt_id asc
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
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'wire_input'),
				e.raw_payload_hash,
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'normalized_input'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'provider_evidence'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'environment_evidence'),
			coalesce(r.receipt_id, 0),
			coalesce(r.received_at, e.recorded_at),
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

// GetReceipt loads one durable intake record by receipt id.
func (s *Store) GetReceipt(ctx context.Context, receiptID int64) (Record, error) {
	return s.receiptRecord(ctx, receiptID, "")
}

func (s *Store) init(ctx context.Context) error {
	if err := auditstorage.Migrate(ctx, s.db); err != nil {
		return wrapLoggedError(ctx, s.log, "migrate intake sqlite schema", err)
	}
	logDeferredReceiptRepairs(ctx, s.db, s.log)
	return nil
}

func (s *Store) withExistingReceipt(
	ctx context.Context,
	expectedEventID string,
	receiptID int64,
	run func(*sql.Tx, string) error,
) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "begin intake state tx", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	var eventID string
	err = tx.QueryRowContext(ctx, `
		select event_id from intake_receipts where receipt_id = ?
	`, receiptID).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrEventNotFound
	}
	if err != nil {
		return wrapLoggedError(ctx, s.log, "lookup intake receipt", err)
	}
	if expectedEventID != "" && expectedEventID != eventID {
		return ErrReceiptEventMismatch
	}
	if err := run(tx, eventID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return wrapLoggedError(ctx, s.log, "commit intake state tx", err)
	}
	return nil
}

func (s *Store) noteReplay(ctx context.Context, receiptID int64) error {
	now := intakeNow().UTC().Format(time.RFC3339Nano)
	return s.withExistingReceipt(ctx, "", receiptID, func(tx *sql.Tx, _ string) error {
		result, err := tx.ExecContext(ctx, `
			update intake_deferred
			set last_replay_at = ?, replay_count = replay_count + 1
			where receipt_id = ? and state = ?
		`, now, receiptID, DeferredStatePending)
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

func (s *Store) pendingRecord(ctx context.Context, receiptID int64) (Record, error) {
	return s.receiptRecord(ctx, receiptID, string(DeferredStatePending))
}

func (s *Store) receiptRecord(ctx context.Context, receiptID int64, requiredState string) (Record, error) {
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
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'wire_input'),
				e.raw_payload_hash,
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'normalized_input'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'provider_evidence'),
				(select content from intake_event_details
					where event_id = e.event_id and detail_class = 'environment_evidence'),
			r.receipt_id,
			r.received_at,
			coalesce(d.state, 'none'),
			d.pending_at,
			d.completed_at,
			d.last_replay_at,
			coalesce(d.replay_count, 0)
		from intake_events e
		join intake_receipts r on r.event_id = e.event_id
		left join intake_deferred d on d.receipt_id = r.receipt_id
		where r.receipt_id = ?`
	var rows *sql.Rows
	var err error
	if requiredState != "" {
		query += ` and d.state = ?`
		rows, err = s.db.QueryContext(ctx, query, receiptID, requiredState)
	} else {
		rows, err = s.db.QueryContext(ctx, query, receiptID)
	}
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
		receivedAt     string
		normalized     string
		classification string
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
		&classification,
		&envFingerprint,
		&record.ReceiptID,
		&receivedAt,
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
	record.ReceivedAt, err = time.Parse(time.RFC3339Nano, receivedAt)
	if err != nil {
		return Record{}, wrapError("parse intake received_at", err)
	}
	record.NormalizedJSON = json.RawMessage(normalized)
	record.ClassificationJSON = json.RawMessage(classification)
	record.RawPayload = make([]byte, len(rawPayload))
	copy(record.RawPayload, rawPayload)
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
	rawPayload := make([]byte, len(record.RawPayload))
	copy(rawPayload, record.RawPayload)
	record.RawPayload = rawPayload
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
	writeHashPart(hash, string(record.ClassificationJSON))
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
