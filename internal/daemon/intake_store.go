package daemon

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

type intakeStore interface {
	Append(context.Context, intake.Record) (intake.AppendResult, error)
	Get(context.Context, string) (intake.Record, error)
	MarkDeferredPending(context.Context, string) error
	MarkDeferredComplete(context.Context, string) error
	ReplayPending(context.Context, func(intake.Record) error) error
	ListPending(context.Context) ([]string, error)
	UpdateHotEvalLatency(context.Context, string, int64) error
	Close() error
}

type sqliteIntakeStore struct {
	store *intake.Store
	log   *slog.Logger
}

func newSQLiteIntakeStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (*sqliteIntakeStore, error) {
	path := intake.DefaultSQLitePath()
	if cfg != nil {
		path = cfg.AuditSQLitePath()
	}
	store, err := intake.OpenSQLite(ctx, path, log)
	if err != nil {
		if log != nil {
			log.WarnContext(ctx, "open sqlite intake store failed", "path", path, "err", err)
		}
		return nil, fmt.Errorf("open sqlite intake store: %w", err)
	}
	return &sqliteIntakeStore{store: store, log: log}, nil
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

func (s *sqliteIntakeStore) MarkDeferredPending(ctx context.Context, eventID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.MarkDeferredPending(ctx, eventID); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "mark intake record pending failed", "event_id", eventID, "err", err)
		}
		return fmt.Errorf("mark intake record %q pending: %w", eventID, err)
	}
	return nil
}

func (s *sqliteIntakeStore) MarkDeferredComplete(ctx context.Context, eventID string) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.MarkDeferredComplete(ctx, eventID); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "mark intake record complete failed", "event_id", eventID, "err", err)
		}
		return fmt.Errorf("mark intake record %q complete: %w", eventID, err)
	}
	return nil
}

func (s *sqliteIntakeStore) ReplayPending(ctx context.Context, replay func(intake.Record) error) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("intake store is nil")
	}
	if err := s.store.ReplayDeferredPending(ctx, 0, replay); err != nil {
		if s.log != nil {
			s.log.WarnContext(ctx, "replay pending intake records failed", "err", err)
		}
		return fmt.Errorf("replay pending intake records: %w", err)
	}
	return nil
}

func (s *sqliteIntakeStore) ListPending(ctx context.Context) ([]string, error) {
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
	eventIDs := make([]string, 0, len(records))
	for _, record := range records {
		eventIDs = append(eventIDs, record.EventID)
	}
	return eventIDs, nil
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

func (s *sqliteIntakeStore) Close() error {
	if s == nil || s.store == nil {
		return nil
	}
	if err := s.store.Close(); err != nil {
		if s.log != nil {
			s.log.Warn("close intake store failed", "err", err)
		}
		return fmt.Errorf("close intake store: %w", err)
	}
	return nil
}
