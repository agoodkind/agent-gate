package intake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/audit"
)

// ErrDeferredAuditClaimUnavailable means another processor owns the live lease.
var ErrDeferredAuditClaimUnavailable = errors.New("deferred audit claim unavailable")

// ErrDeferredAuditClaimLost means a processor no longer owns the delivery attempt.
var ErrDeferredAuditClaimLost = errors.New("deferred audit claim lost")

func ensureDeferredAuditOutboxSchema(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("begin deferred audit outbox migration", err)
	}
	defer func() { _ = transaction.Rollback() }()
	statements := []string{
		`create table if not exists deferred_audit_outbox (
			receipt_id integer primary key,
			event_id text not null,
			evaluation_id text not null unique,
			state text not null,
			created_at text not null,
			completed_at text,
			claim_owner text,
			claim_expires_at text,
			claim_attempt integer not null default 0,
			foreign key(receipt_id, event_id)
				references intake_receipts(receipt_id, event_id) on delete cascade,
			foreign key(evaluation_id) references gate_evaluations(evaluation_id)
				on delete cascade,
			check(state in ('pending', 'complete'))
		)`,
		`create table if not exists deferred_audit_outbox_entries (
			receipt_id integer not null,
			entry_index integer not null,
			audit_event_id text not null,
			payload_json blob not null,
			delivered_at text,
			primary key(receipt_id, entry_index),
			foreign key(receipt_id) references deferred_audit_outbox(receipt_id)
				on delete cascade
		)`,
		`create index if not exists deferred_audit_outbox_pending_idx
			on deferred_audit_outbox(state, claim_expires_at)`,
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return wrapError("initialize deferred audit outbox", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return wrapError("commit deferred audit outbox migration", err)
	}
	return nil
}

// ListPendingDeferredAudit returns receipt ids with undelivered audit entries.
func (s *Store) ListPendingDeferredAudit(ctx context.Context, limit int) ([]int64, error) {
	query := `select receipt_id from deferred_audit_outbox
		where state = 'pending' order by receipt_id`
	arguments := make([]any, 0, 1)
	if limit > 0 {
		query += " limit ?"
		arguments = append(arguments, limit)
	}
	rows, err := s.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, wrapLoggedError(ctx, s.log, "list pending deferred audit", err)
	}
	defer func() { _ = rows.Close() }()
	receiptIDs := make([]int64, 0)
	for rows.Next() {
		var receiptID int64
		if err := rows.Scan(&receiptID); err != nil {
			return nil, wrapLoggedError(ctx, s.log, "scan pending deferred audit", err)
		}
		receiptIDs = append(receiptIDs, receiptID)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapLoggedError(ctx, s.log, "iterate pending deferred audit", err)
	}
	return receiptIDs, nil
}

// ClaimDeferredAudit leases one receipt's ordered, undelivered audit entries.
func (s *Store) ClaimDeferredAudit(
	ctx context.Context,
	receiptID int64,
	owner string,
	leaseDuration time.Duration,
) ([]DeferredAuditEntry, DeferredAuditClaim, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return nil, DeferredAuditClaim{}, errors.New("deferred audit claim owner is required")
	}
	if leaseDuration <= 0 {
		return nil, DeferredAuditClaim{}, errors.New("deferred audit claim lease must be positive")
	}
	now := intakeNow().UTC()
	expiresAt := now.Add(leaseDuration)
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, DeferredAuditClaim{}, wrapLoggedError(ctx, s.log, "begin deferred audit claim", err)
	}
	defer func() { _ = transaction.Rollback() }()
	result, err := transaction.ExecContext(ctx, `
		update deferred_audit_outbox
		set claim_owner = ?, claim_expires_at = ?, claim_attempt = claim_attempt + 1
		where receipt_id = ? and state = 'pending'
			and (claim_owner is null or claim_expires_at is null or claim_expires_at <= ?)
	`, owner, formatDeferredTime(expiresAt), receiptID, formatDeferredTime(now))
	if err != nil {
		return nil, DeferredAuditClaim{}, wrapLoggedError(ctx, s.log, "claim deferred audit", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return nil, DeferredAuditClaim{}, wrapLoggedError(ctx, s.log, "read deferred audit claim rows", err)
	}
	if rowsAffected != 1 {
		return nil, DeferredAuditClaim{}, ErrDeferredAuditClaimUnavailable
	}
	var eventID string
	var attempt int
	if err := transaction.QueryRowContext(ctx, `
		select event_id, claim_attempt from deferred_audit_outbox where receipt_id = ?
	`, receiptID).Scan(&eventID, &attempt); err != nil {
		return nil, DeferredAuditClaim{}, wrapLoggedError(ctx, s.log, "read deferred audit claim", err)
	}
	entries, err := readDeferredAuditEntries(ctx, transaction, receiptID)
	if err != nil {
		return nil, DeferredAuditClaim{}, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, DeferredAuditClaim{}, wrapLoggedError(ctx, s.log, "commit deferred audit claim", err)
	}
	claim := DeferredAuditClaim{
		ReceiptID: receiptID, EventID: eventID, Owner: owner,
		Attempt: attempt, ExpiresAt: expiresAt,
	}
	return entries, claim, nil
}

func readDeferredAuditEntries(
	ctx context.Context,
	transaction *sql.Tx,
	receiptID int64,
) ([]DeferredAuditEntry, error) {
	rows, err := transaction.QueryContext(ctx, `
		select entry_index, audit_event_id, payload_json
		from deferred_audit_outbox_entries
		where receipt_id = ? and delivered_at is null order by entry_index
	`, receiptID)
	if err != nil {
		return nil, wrapError("read deferred audit entries", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]DeferredAuditEntry, 0)
	for rows.Next() {
		var entry DeferredAuditEntry
		var auditEventID string
		var payload []byte
		if err := rows.Scan(&entry.Index, &auditEventID, &payload); err != nil {
			return nil, wrapError("scan deferred audit entry", err)
		}
		if err := json.Unmarshal(payload, &entry.Entry); err != nil {
			return nil, wrapError("decode deferred audit entry", err)
		}
		if entry.Entry.Event.EventID != auditEventID || entry.Entry.Fingerprint == "" {
			return nil, errors.New("deferred audit event identity mismatch")
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate deferred audit entries", err)
	}
	return entries, nil
}

// RenewDeferredAuditClaim extends a live delivery lease.
func (s *Store) RenewDeferredAuditClaim(
	ctx context.Context,
	claim DeferredAuditClaim,
	leaseDuration time.Duration,
) error {
	if leaseDuration <= 0 {
		return errors.New("deferred audit claim lease must be positive")
	}
	now := intakeNow().UTC()
	result, err := s.db.ExecContext(ctx, `
		update deferred_audit_outbox set claim_expires_at = ?
		where receipt_id = ? and event_id = ? and state = 'pending'
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
	`, formatDeferredTime(now.Add(leaseDuration)), claim.ReceiptID, claim.EventID,
		claim.Owner, claim.Attempt, formatDeferredTime(now))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "renew deferred audit claim", err)
	}
	return deferredAuditClaimResult(result)
}

// MarkDeferredAuditEntryDelivered acknowledges one entry under a live claim.
func (s *Store) MarkDeferredAuditEntryDelivered(
	ctx context.Context,
	claim DeferredAuditClaim,
	entryIndex int,
) error {
	now := intakeNow().UTC()
	result, err := s.db.ExecContext(ctx, `
		update deferred_audit_outbox_entries set delivered_at = ?
		where receipt_id = ? and entry_index = ? and delivered_at is null
			and exists (
				select 1 from deferred_audit_outbox outbox
				where outbox.receipt_id = deferred_audit_outbox_entries.receipt_id
					and outbox.event_id = ? and outbox.state = 'pending'
					and outbox.claim_owner = ? and outbox.claim_attempt = ?
					and outbox.claim_expires_at > ?
			)
	`, formatDeferredTime(now), claim.ReceiptID, entryIndex, claim.EventID,
		claim.Owner, claim.Attempt, formatDeferredTime(now))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "mark deferred audit entry delivered", err)
	}
	return deferredAuditClaimResult(result)
}

// CompleteDeferredAudit completes an outbox after every entry is delivered.
func (s *Store) CompleteDeferredAudit(ctx context.Context, claim DeferredAuditClaim) error {
	now := intakeNow().UTC()
	result, err := s.db.ExecContext(ctx, `
		update deferred_audit_outbox
		set state = 'complete', completed_at = ?, claim_owner = null, claim_expires_at = null
		where receipt_id = ? and event_id = ? and state = 'pending'
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
			and not exists (
				select 1 from deferred_audit_outbox_entries entry
				where entry.receipt_id = deferred_audit_outbox.receipt_id
					and entry.delivered_at is null
			)
	`, formatDeferredTime(now), claim.ReceiptID, claim.EventID, claim.Owner,
		claim.Attempt, formatDeferredTime(now))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "complete deferred audit", err)
	}
	return deferredAuditClaimResult(result)
}

// ReleaseDeferredAuditClaim makes failed delivery immediately retryable.
func (s *Store) ReleaseDeferredAuditClaim(ctx context.Context, claim DeferredAuditClaim) error {
	result, err := s.db.ExecContext(ctx, `
		update deferred_audit_outbox set claim_owner = null, claim_expires_at = null
		where receipt_id = ? and event_id = ? and state = 'pending'
			and claim_owner = ? and claim_attempt = ?
	`, claim.ReceiptID, claim.EventID, claim.Owner, claim.Attempt)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "release deferred audit claim", err)
	}
	return deferredAuditClaimResult(result)
}

func deferredAuditClaimResult(result sql.Result) error {
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return wrapError("read deferred audit claim result", err)
	}
	if rowsAffected != 1 {
		return ErrDeferredAuditClaimLost
	}
	return nil
}

func insertDeferredAuditOutbox(
	ctx context.Context,
	transaction *sql.Tx,
	claim DeferredClaim,
	evaluationID string,
	entries []audit.NormalizedEntry,
) error {
	if len(entries) == 0 {
		return nil
	}
	now := formatDeferredTime(intakeNow().UTC())
	if _, err := transaction.ExecContext(ctx, `
		insert into deferred_audit_outbox (
			receipt_id, event_id, evaluation_id, state, created_at, completed_at,
			claim_owner, claim_expires_at, claim_attempt
		) values (?, ?, ?, 'pending', ?, null, cast(null as text), null, 0)
	`, claim.ReceiptID, claim.EventID, evaluationID, now); err != nil {
		return wrapError("insert deferred audit outbox", err)
	}
	for index, entry := range entries {
		if entry.Event.EventID == "" || entry.Fingerprint == "" {
			return errors.New("normalized deferred audit entry identity is required")
		}
		payload, err := json.Marshal(entry)
		if err != nil {
			return wrapError("encode deferred audit entry", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			insert into deferred_audit_outbox_entries (
				receipt_id, entry_index, audit_event_id, payload_json, delivered_at
			) values (?, ?, ?, ?, null)
		`, claim.ReceiptID, index, entry.Event.EventID, payload); err != nil {
			return wrapError("insert deferred audit entry", err)
		}
	}
	return nil
}
