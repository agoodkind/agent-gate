package intake

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// PendingMetadata excludes the receipt payload from recovery discovery.
type PendingMetadata struct {
	ReceiptID      int64
	State          DeferredState
	NextAttemptAt  *time.Time
	ClaimOwner     string
	ClaimExpiresAt *time.Time
}

// PendingCursor selects one index range within a frozen recovery pass.
type PendingCursor struct {
	MaximumReceiptID int64
	EligibilityTime  time.Time
	RetryTimes       bool
	AfterTime        string
	AfterReceiptID   int64
}

// BeginPending captures the receipt ceiling without retaining a transaction.
func (s *Store) BeginPending(ctx context.Context, at time.Time) (PendingCursor, error) {
	var maximum int64
	err := s.db.QueryRowContext(ctx, pendingMaximumSQL).Scan(&maximum)
	return PendingCursor{MaximumReceiptID: maximum, EligibilityTime: at, RetryTimes: false, AfterTime: "", AfterReceiptID: 0}, wrapPendingError("capture pending receipt ceiling", err)
}

// PendingPage returns at most 100 raw index entries. Callers advance across
// entries above MaximumReceiptID in the retry-time range as well.
func (s *Store) PendingPage(ctx context.Context, cursor PendingCursor) ([]PendingMetadata, error) {
	var rows *sql.Rows
	var err error
	if cursor.RetryTimes {
		rows, err = s.db.QueryContext(ctx, pendingRetryPageSQL, cursor.AfterTime, cursor.AfterReceiptID, auditstorage.FormatTime(cursor.EligibilityTime))
	} else {
		rows, err = s.db.QueryContext(ctx, pendingNullPageSQL, cursor.AfterReceiptID, cursor.MaximumReceiptID)
	}
	if err != nil {
		return nil, wrapError("read pending metadata", err)
	}
	defer func() { _ = rows.Close() }()
	entries := make([]PendingMetadata, 0, 100)
	for rows.Next() {
		var entry PendingMetadata
		var retry, owner, expires sql.NullString
		if err := rows.Scan(&entry.ReceiptID, &entry.State, &retry, &owner, &expires); err != nil {
			return nil, wrapError("scan pending metadata", err)
		}
		_, err := decodePendingMetadata(&entry, retry, owner, expires)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, wrapPendingError("iterate pending metadata", rows.Err())
}

// Advance includes every raw entry, including entries excluded by the ceiling.
func (cursor *PendingCursor) Advance(entry PendingMetadata) {
	cursor.AfterReceiptID = entry.ReceiptID
	if entry.NextAttemptAt != nil {
		cursor.AfterTime = auditstorage.FormatTime(*entry.NextAttemptAt)
	}
}

// FirstFuturePending uses one indexed lookup for the next retry deadline.
func (s *Store) FirstFuturePending(ctx context.Context, at time.Time) (*PendingMetadata, error) {
	return readPendingRow(s.db.QueryRowContext(ctx, pendingFutureSQL, auditstorage.FormatTime(at)))
}

// PendingReceipt reads completion and retry metadata without decoding a payload.
func (s *Store) PendingReceipt(ctx context.Context, receiptID int64) (*PendingMetadata, error) {
	return readPendingRow(s.db.QueryRowContext(ctx, pendingReceiptSQL, receiptID))
}

// HasPending includes future retries and live claims when checking completion.
func (s *Store) HasPending(ctx context.Context) (bool, error) {
	var pending bool
	err := s.db.QueryRowContext(ctx, hasPendingSQL).Scan(&pending)
	return pending, wrapPendingError("check pending receipts", err)
}

func readPendingRow(row *sql.Row) (*PendingMetadata, error) {
	var entry PendingMetadata
	var retry, owner, expires sql.NullString
	err := row.Scan(&entry.ReceiptID, &entry.State, &retry, &owner, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapError("read pending receipt metadata", err)
	}
	return decodePendingMetadata(&entry, retry, owner, expires)
}

func decodePendingMetadata(entry *PendingMetadata, retry, owner, expires sql.NullString) (*PendingMetadata, error) {
	var err error
	entry.ClaimOwner = owner.String
	entry.NextAttemptAt, err = parseOptionalTime(retry)
	if err != nil {
		return nil, err
	}
	entry.ClaimExpiresAt, err = parseOptionalTime(expires)
	return entry, err
}

func wrapPendingError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return wrapError(operation, err)
}

//go:embed pending_maximum.sql
var pendingMaximumSQL string

//go:embed pending_null_page.sql
var pendingNullPageSQL string

//go:embed pending_retry_page.sql
var pendingRetryPageSQL string

//go:embed pending_future.sql
var pendingFutureSQL string

//go:embed pending_receipt.sql
var pendingReceiptSQL string

//go:embed has_pending.sql
var hasPendingSQL string
