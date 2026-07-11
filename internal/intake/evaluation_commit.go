package intake

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/evaluation"
)

// ErrDeferredClaimUnavailable means another processor owns the live lease.
var ErrDeferredClaimUnavailable = errors.New("deferred claim unavailable")

// ErrDeferredClaimLost means a processor no longer owns the claimed attempt.
var ErrDeferredClaimLost = errors.New("deferred claim lost")

// CommitHotEvaluation atomically stores the hot evaluation and, when needed,
// marks its receipt pending for deferred processing.
func (s *Store) CommitHotEvaluation(
	ctx context.Context,
	eventID string,
	receiptID int64,
	deferredPending bool,
	record evaluation.Record,
) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "begin hot evaluation transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	canonicalEventID, err := receiptEventID(ctx, transaction, receiptID)
	if err != nil {
		return err
	}
	if eventID != canonicalEventID {
		return ErrReceiptEventMismatch
	}
	if deferredPending {
		if err := markDeferredPendingInTx(
			ctx, transaction, receiptID, canonicalEventID, intakeNow().UTC(),
		); err != nil {
			return err
		}
	}
	if err := s.evaluations.RecordCompletedInTx(ctx, transaction, record); err != nil {
		return wrapLoggedError(ctx, s.log, "record completed hot evaluation", err)
	}
	if err := transaction.Commit(); err != nil {
		return wrapLoggedError(ctx, s.log, "commit hot evaluation transaction", err)
	}
	return nil
}

// ClaimDeferred atomically leases one pending receipt and allocates its attempt.
func (s *Store) ClaimDeferred(
	ctx context.Context,
	receiptID int64,
	owner string,
	leaseDuration time.Duration,
) (Record, DeferredClaim, error) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return Record{}, DeferredClaim{}, errors.New("deferred claim owner is required")
	}
	if leaseDuration <= 0 {
		return Record{}, DeferredClaim{}, errors.New("deferred claim lease must be positive")
	}
	now := intakeNow().UTC()
	expiresAt := now.Add(leaseDuration)
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, DeferredClaim{}, wrapLoggedError(ctx, s.log, "begin deferred claim transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	result, err := transaction.ExecContext(ctx, `
		update intake_deferred
		set claim_owner = ?, claim_expires_at = ?,
			claim_attempt = claim_attempt + 1,
			last_replay_at = ?, replay_count = replay_count + 1
		where receipt_id = ? and state = ?
			and (claim_owner is null or claim_expires_at is null or claim_expires_at <= ?)
	`, owner, formatDeferredTime(expiresAt), formatDeferredTime(now), receiptID,
		DeferredStatePending, formatDeferredTime(now))
	if err != nil {
		return Record{}, DeferredClaim{}, wrapLoggedError(ctx, s.log, "claim deferred receipt", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return Record{}, DeferredClaim{}, wrapLoggedError(ctx, s.log, "read deferred claim rows", err)
	}
	if rowsAffected != 1 {
		return Record{}, DeferredClaim{}, ErrDeferredClaimUnavailable
	}
	var eventID string
	var attempt int
	if err := transaction.QueryRowContext(ctx, `
		select event_id, claim_attempt from intake_deferred where receipt_id = ?
	`, receiptID).Scan(&eventID, &attempt); err != nil {
		return Record{}, DeferredClaim{}, wrapLoggedError(ctx, s.log, "read deferred claim", err)
	}
	if err := transaction.Commit(); err != nil {
		return Record{}, DeferredClaim{}, wrapLoggedError(ctx, s.log, "commit deferred claim", err)
	}
	record, err := s.pendingRecord(ctx, receiptID)
	if err != nil {
		return Record{}, DeferredClaim{}, err
	}
	claim := DeferredClaim{
		ReceiptID: receiptID, EventID: eventID, Owner: owner,
		Attempt: attempt, ExpiresAt: expiresAt,
	}
	return record, claim, nil
}

// RenewDeferredClaim extends a live lease held by the same owner and attempt.
func (s *Store) RenewDeferredClaim(
	ctx context.Context,
	claim DeferredClaim,
	leaseDuration time.Duration,
) error {
	if leaseDuration <= 0 {
		return errors.New("deferred claim lease must be positive")
	}
	now := intakeNow().UTC()
	result, err := s.db.ExecContext(ctx, `
		update intake_deferred
		set claim_expires_at = ?
		where receipt_id = ? and event_id = ? and state = ?
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
	`, formatDeferredTime(now.Add(leaseDuration)), claim.ReceiptID, claim.EventID,
		DeferredStatePending, claim.Owner, claim.Attempt, formatDeferredTime(now))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "renew deferred claim", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return wrapLoggedError(ctx, s.log, "read renewed deferred claim rows", err)
	}
	if rowsAffected != 1 {
		return ErrDeferredClaimLost
	}
	return nil
}

// ReleaseDeferredClaim makes a failed attempt immediately retryable.
func (s *Store) ReleaseDeferredClaim(ctx context.Context, claim DeferredClaim) error {
	result, err := s.db.ExecContext(ctx, `
		update intake_deferred
		set claim_owner = null, claim_expires_at = null
		where receipt_id = ? and event_id = ? and state = ?
			and claim_owner = ? and claim_attempt = ?
	`, claim.ReceiptID, claim.EventID, DeferredStatePending, claim.Owner, claim.Attempt)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "release deferred claim", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return wrapLoggedError(ctx, s.log, "read released deferred claim rows", err)
	}
	if rowsAffected != 1 {
		return ErrDeferredClaimLost
	}
	return nil
}

// CommitDeferredEvaluation atomically stores one claimed evaluation and marks
// the receipt complete.
func (s *Store) CommitDeferredEvaluation(
	ctx context.Context,
	claim DeferredClaim,
	record evaluation.Record,
	auditEntries []audit.NormalizedEntry,
) error {
	transaction, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrapLoggedError(ctx, s.log, "begin deferred evaluation transaction", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	now := intakeNow().UTC()
	result, err := transaction.ExecContext(ctx, `
		update intake_deferred
		set state = ?, completed_at = ?, claim_owner = null, claim_expires_at = null
		where receipt_id = ? and event_id = ? and state = ?
			and claim_owner = ? and claim_attempt = ? and claim_expires_at > ?
	`, DeferredStateComplete, formatDeferredTime(now), claim.ReceiptID, claim.EventID,
		DeferredStatePending, claim.Owner, claim.Attempt, formatDeferredTime(now))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "complete claimed deferred receipt", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return wrapLoggedError(ctx, s.log, "read completed deferred claim rows", err)
	}
	if rowsAffected != 1 {
		return ErrDeferredClaimLost
	}
	if record.Evaluation.ReceiptID != claim.ReceiptID ||
		record.Evaluation.EventID != claim.EventID ||
		record.Evaluation.Attempt != claim.Attempt {
		return errors.New("deferred evaluation does not match claim")
	}
	if err := s.evaluations.RecordCompletedInTx(ctx, transaction, record); err != nil {
		return wrapLoggedError(ctx, s.log, "record completed deferred evaluation", err)
	}
	if err := insertDeferredAuditOutbox(
		ctx, transaction, claim, record.Evaluation.EvaluationID, auditEntries,
	); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrapLoggedError(ctx, s.log, "commit deferred evaluation transaction", err)
	}
	return nil
}

func markDeferredPendingInTx(
	ctx context.Context,
	transaction *sql.Tx,
	receiptID int64,
	eventID string,
	now time.Time,
) error {
	_, err := transaction.ExecContext(ctx, `
		insert into intake_deferred (
			receipt_id, event_id, state, pending_at, completed_at,
			last_replay_at, replay_count, claim_owner, claim_expires_at, claim_attempt
		) values (?, ?, ?, ?, null, cast(null as text), 0, null, cast(null as text), 0)
		on conflict(receipt_id) do update set
			state = excluded.state,
			pending_at = coalesce(intake_deferred.pending_at, excluded.pending_at),
			completed_at = null,
			claim_owner = null,
			claim_expires_at = null
	`, receiptID, eventID, DeferredStatePending, formatDeferredTime(now))
	if err != nil {
		return wrapError("mark deferred pending in transaction", err)
	}
	return nil
}

func receiptEventID(ctx context.Context, transaction *sql.Tx, receiptID int64) (string, error) {
	var eventID string
	err := transaction.QueryRowContext(ctx, `
		select event_id from intake_receipts where receipt_id = ?
	`, receiptID).Scan(&eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrEventNotFound
	}
	if err != nil {
		return "", wrapError("lookup intake receipt", err)
	}
	return eventID, nil
}

func formatDeferredTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
