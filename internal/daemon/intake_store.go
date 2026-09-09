package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

type intakeStore interface {
	Append(context.Context, intake.Record) (intake.AppendResult, error)
	Get(context.Context, string) (intake.Record, error)
	GetReceipt(context.Context, int64) (intake.Record, error)
	ClaimDeferred(
		context.Context, int64, string, time.Duration,
	) (intake.Record, intake.DeferredClaim, error)
	RenewDeferredClaim(context.Context, intake.DeferredClaim, time.Duration) error
	ReleaseDeferredClaim(context.Context, intake.DeferredClaim) error
	ScheduleDeferredRetry(context.Context, intake.DeferredClaim, time.Time) error
	ListPending(context.Context) ([]int64, error)
}

type sqliteEvaluationRecorder struct {
	store *intake.Store
	log   *slog.Logger
}

func (recorder sqliteEvaluationRecorder) RecordCompleted(
	ctx context.Context,
	record evaluation.Record,
) error {
	if err := recorder.store.Evaluations().RecordCompleted(ctx, record); err != nil {
		if recorder.log != nil {
			recorder.log.WarnContext(ctx, "record completed evaluation failed", "err", err)
		}
		return fmt.Errorf("record completed evaluation: %w", err)
	}
	return nil
}

func (recorder sqliteEvaluationRecorder) CommitHotEvaluation(
	ctx context.Context,
	eventID string,
	receiptID int64,
	deferredPending bool,
	record evaluation.Record,
	auditEntries []audit.NormalizedEntry,
) error {
	if err := recorder.store.CommitHotEvaluation(
		ctx, eventID, receiptID, deferredPending, record, auditEntries,
	); err != nil {
		if recorder.log != nil {
			recorder.log.WarnContext(ctx, "commit hot evaluation failed", "err", err)
		}
		return fmt.Errorf("commit hot evaluation: %w", err)
	}
	return nil
}

func (recorder sqliteEvaluationRecorder) CommitDeferredEvaluation(
	ctx context.Context,
	claim intake.DeferredClaim,
	record evaluation.Record,
	auditEntries []audit.NormalizedEntry,
) error {
	if err := recorder.store.CommitDeferredEvaluation(ctx, claim, record, auditEntries); err != nil {
		if recorder.log != nil {
			recorder.log.WarnContext(ctx, "commit deferred evaluation failed", "err", err)
		}
		return fmt.Errorf("commit deferred evaluation: %w", err)
	}
	return nil
}

func (s *sqliteIntakeStore) GetReceipt(ctx context.Context, receiptID int64) (intake.Record, error) {
	if s == nil || s.store == nil {
		return intake.Record{}, fmt.Errorf("intake store is nil")
	}
	record, err := s.store.GetReceipt(ctx, receiptID)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "get intake receipt failed", "receipt_id", receiptID, "err", err)
		}
		return intake.Record{}, fmt.Errorf("get intake receipt %d: %w", receiptID, err)
	}
	return record, nil
}

type sqliteIntakeStore struct {
	store *intake.Store
	log   *slog.Logger
}

func (s *sqliteIntakeStore) ScheduleDeferredRetry(ctx context.Context, claim intake.DeferredClaim, nextAttempt time.Time) error {
	return wrapServerError("schedule deferred retry", s.store.ScheduleDeferredRetry(ctx, claim, nextAttempt))
}

func (s *sqliteIntakeStore) Append(ctx context.Context, record intake.Record) (intake.AppendResult, error) {
	if s == nil || s.store == nil {
		return intake.AppendResult{}, fmt.Errorf("intake store is nil")
	}
	result, err := s.store.Append(ctx, record)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "append intake record failed", "err", err)
		}
		return intake.AppendResult{}, fmt.Errorf("append intake record: %w", err)
	}
	return result, nil
}

func (s *sqliteIntakeStore) Get(ctx context.Context, eventID string) (intake.Record, error) {
	if s == nil || s.store == nil {
		return intake.Record{}, fmt.Errorf("intake store is nil")
	}
	record, err := s.store.Get(ctx, eventID)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "get intake record failed", "event_id", eventID, "err", err)
		}
		return intake.Record{}, fmt.Errorf("get intake record %q: %w", eventID, err)
	}
	return record, nil
}

func (s *sqliteIntakeStore) MarkDeferredPending(ctx context.Context, eventID string, receiptID int64) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.MarkDeferredPending(ctx, eventID, receiptID); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "mark intake record pending failed", "event_id", eventID, "err", err)
		}
		return fmt.Errorf("mark intake record %q pending: %w", eventID, err)
	}
	return nil
}

func (s *sqliteIntakeStore) MarkDeferredComplete(ctx context.Context, receiptID int64) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.MarkDeferredComplete(ctx, receiptID); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "mark intake record complete failed", "receipt_id", receiptID, "err", err)
		}
		return fmt.Errorf("mark intake receipt %d complete: %w", receiptID, err)
	}
	return nil
}

func (s *sqliteIntakeStore) ClaimDeferred(
	ctx context.Context,
	receiptID int64,
	owner string,
	leaseDuration time.Duration,
) (intake.Record, intake.DeferredClaim, error) {
	if s == nil || s.store == nil {
		return intake.Record{}, intake.DeferredClaim{}, fmt.Errorf("intake store is nil")
	}
	record, claim, err := s.store.ClaimDeferred(ctx, receiptID, owner, leaseDuration)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "claim deferred receipt failed", "receipt_id", receiptID, "err", err)
		}
		return intake.Record{}, intake.DeferredClaim{}, fmt.Errorf("claim deferred receipt: %w", err)
	}
	return record, claim, nil
}

func (s *sqliteIntakeStore) ReleaseDeferredClaim(
	ctx context.Context,
	claim intake.DeferredClaim,
) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.ReleaseDeferredClaim(ctx, claim); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "release deferred claim failed", "receipt_id", claim.ReceiptID, "err", err)
		}
		return fmt.Errorf("release deferred claim: %w", err)
	}
	return nil
}

func (s *sqliteIntakeStore) RenewDeferredClaim(
	ctx context.Context,
	claim intake.DeferredClaim,
	leaseDuration time.Duration,
) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.RenewDeferredClaim(ctx, claim, leaseDuration); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "renew deferred claim failed", "receipt_id", claim.ReceiptID, "err", err)
		}
		return fmt.Errorf("renew deferred claim: %w", err)
	}
	return nil
}

func (s *sqliteIntakeStore) ListPending(ctx context.Context) ([]int64, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("intake store is nil")
	}
	records, err := s.store.ListDeferredPending(ctx, 0)
	if err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "list pending intake records failed", "err", err)
		}
		return nil, fmt.Errorf("list pending intake records: %w", err)
	}
	receiptIDs := make([]int64, 0, len(records))
	for _, record := range records {
		receiptIDs = append(receiptIDs, record.ReceiptID)
	}
	return receiptIDs, nil
}

func (s *sqliteIntakeStore) UpdateHotEvalLatency(ctx context.Context, eventID string, latencyMicros int64) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.UpdateHotEvalLatency(ctx, eventID, latencyMicros); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "update intake hot eval latency failed", "event_id", eventID, "err", err)
		}
		return fmt.Errorf("update intake hot_eval_latency_us %q: %w", eventID, err)
	}
	return nil
}

func (s *sqliteIntakeStore) Handle() *sql.DB {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.Handle()
}

func (s *sqliteIntakeStore) Evaluations() evaluationRecorder {
	if s == nil || s.store == nil {
		return nil
	}
	return sqliteEvaluationRecorder{store: s.store, log: s.log}
}
