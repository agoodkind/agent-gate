package intake

import (
	"context"
	"errors"
)

// ReplayDeferredPending walks pending records in order and records replay
// metadata before invoking replay.
func (s *Store) ReplayDeferredPending(
	ctx context.Context,
	limit int,
	replay func(Record) error,
) error {
	records, err := s.ListDeferredPending(ctx, limit)
	if err != nil {
		return err
	}
	for _, record := range records {
		if err := s.noteReplay(ctx, record.ReceiptID); err != nil {
			if errors.Is(err, ErrEventNotFound) {
				continue
			}
			return err
		}
		refreshed, err := s.pendingRecord(ctx, record.ReceiptID)
		if err != nil {
			if errors.Is(err, ErrEventNotFound) {
				continue
			}
			return err
		}
		if err := replay(refreshed); err != nil {
			return err
		}
	}
	return nil
}
