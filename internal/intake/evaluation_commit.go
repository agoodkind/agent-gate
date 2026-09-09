package intake

import (
	"context"
	"database/sql"
	_ "embed"
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
	if err := s.clearTerminalInput(ctx, transaction, canonicalEventID); err != nil {
		return err
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
	result, err := transaction.ExecContext(ctx, evaluationCommitSQL1, owner, formatDeferredTime(expiresAt), formatDeferredTime(now), receiptID,
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
	if err := transaction.QueryRowContext(ctx, evaluationCommitSQL2, receiptID).Scan(&eventID, &attempt); err != nil {
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
	result, err := s.db.ExecContext(ctx, evaluationCommitSQL3, formatDeferredTime(now.Add(leaseDuration)), claim.ReceiptID, claim.EventID,
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
	result, err := s.db.ExecContext(ctx, evaluationCommitSQL4, claim.ReceiptID, claim.EventID, DeferredStatePending, claim.Owner, claim.Attempt)
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
	result, err := transaction.ExecContext(ctx, evaluationCommitSQL5, DeferredStateComplete, formatDeferredTime(now), claim.ReceiptID, claim.EventID,
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
	events := make([]audit.Event, 0, len(auditEntries))
	for _, entry := range auditEntries {
		events = append(events, entry.Event)
	}
	if err := audit.WriteEventsInTx(ctx, transaction, events); err != nil {
		return wrapError("write completed deferred audit", err)
	}
	if err := s.clearTerminalInput(ctx, transaction, claim.EventID); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return wrapLoggedError(ctx, s.log, "commit deferred evaluation transaction", err)
	}
	return nil
}

//go:embed clear_terminal_input.sql
var clearTerminalInputSQL string

func (s *Store) clearTerminalInput(ctx context.Context, transaction *sql.Tx, eventID string) error {
	if _, err := transaction.ExecContext(ctx, clearTerminalInputSQL, eventID); err != nil {
		return wrapError("clear terminal replay input", err)
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
	_, err := transaction.ExecContext(ctx, evaluationCommitSQL6, receiptID, eventID, DeferredStatePending, formatDeferredTime(now))
	if err != nil {
		return wrapError("mark deferred pending in transaction", err)
	}
	return nil
}

func receiptEventID(ctx context.Context, transaction *sql.Tx, receiptID int64) (string, error) {
	var eventID string
	err := transaction.QueryRowContext(ctx, evaluationCommitSQL7, receiptID).Scan(&eventID)
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

//go:embed evaluation_commit_1.sql
var evaluationCommitSQL1 string

//go:embed evaluation_commit_2.sql
var evaluationCommitSQL2 string

//go:embed evaluation_commit_3.sql
var evaluationCommitSQL3 string

//go:embed evaluation_commit_4.sql
var evaluationCommitSQL4 string

//go:embed evaluation_commit_5.sql
var evaluationCommitSQL5 string

//go:embed evaluation_commit_6.sql
var evaluationCommitSQL6 string

//go:embed evaluation_commit_7.sql
var evaluationCommitSQL7 string
