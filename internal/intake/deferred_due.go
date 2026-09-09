package intake

import (
	"context"
	_ "embed"
	"errors"
	"time"
)

// ListDueDeferred pages identifiers without loading stored hook payloads.
func (s *Store) ListDueDeferred(ctx context.Context, now time.Time, afterReceiptID int64, limit int) ([]int64, error) {
	if limit <= 0 {
		return nil, errors.New("deferred page limit must be positive")
	}
	stamp := formatDeferredTime(now)
	rows, err := s.db.QueryContext(ctx, listDueDeferredSQL, afterReceiptID, stamp, stamp, limit)
	if err != nil {
		return nil, wrapLoggedError(ctx, s.log, "list due deferred receipts", err)
	}
	defer func() { _ = rows.Close() }()
	identifiers := make([]int64, 0, limit)
	for rows.Next() {
		var identifier int64
		if err := rows.Scan(&identifier); err != nil {
			return nil, wrapError("scan due deferred receipt", err)
		}
		identifiers = append(identifiers, identifier)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapError("iterate due deferred receipts", err)
	}
	return identifiers, nil
}

// ScheduleDeferredRetry releases only the live matching attempt at its next due time.
func (s *Store) ScheduleDeferredRetry(ctx context.Context, claim DeferredClaim, nextAttempt time.Time) error {
	result, err := s.db.ExecContext(ctx, scheduleDeferredRetrySQL, formatDeferredTime(nextAttempt), claim.ReceiptID, claim.EventID, claim.Owner, claim.Attempt, formatDeferredTime(intakeNow()))
	if err != nil {
		return wrapLoggedError(ctx, s.log, "schedule deferred retry", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrapError("read scheduled deferred retry", err)
	}
	if count != 1 {
		return ErrDeferredClaimLost
	}
	return nil
}

//go:embed list_due_deferred.sql
var listDueDeferredSQL string

//go:embed schedule_deferred_retry.sql
var scheduleDeferredRetrySQL string
