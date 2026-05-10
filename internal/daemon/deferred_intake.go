package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
)

type deferredProcessor struct {
	events   chan string
	store    intakeStore
	sink     audit.Sink
	cfg      *config.Config
	log      *slog.Logger
	done     chan struct{}
	wg       sync.WaitGroup
	stopping atomic.Bool
}

func newDeferredProcessor(ctx context.Context, store intakeStore, sink audit.Sink, cfg *config.Config, queueLimit int, workers int, log *slog.Logger) *deferredProcessor {
	if queueLimit <= 0 {
		queueLimit = 1
	}
	if log == nil {
		log = slog.Default()
	}

	processor := &deferredProcessor{
		events:   make(chan string, queueLimit),
		store:    store,
		sink:     sink,
		cfg:      cfg,
		log:      log,
		done:     make(chan struct{}),
		wg:       sync.WaitGroup{},
		stopping: atomic.Bool{},
	}

	for range workers {
		processor.wg.Go(func() {
			defer func() {
				if recovered := recover(); recovered != nil && processor.log != nil {
					processor.log.ErrorContext(ctx, "deferred intake worker panic recovered", "err", recovered)
				}
			}()
			processor.worker(ctx)
		})
	}
	return processor
}

func (p *deferredProcessor) ReplayPending(ctx context.Context) error {
	if p == nil || p.store == nil {
		return nil
	}

	if err := p.store.ReplayPending(ctx, func(record intake.Record) error {
		p.processRecord(ctx, record)
		return nil
	}); err != nil {
		if p.log != nil {
			p.log.WarnContext(ctx, "replay pending deferred intake failed", "err", err)
		}
		return fmt.Errorf("replay pending deferred intake: %w", err)
	}
	return nil
}

func (p *deferredProcessor) Enqueue(eventID string) bool {
	if p == nil || p.store == nil || eventID == "" || p.stopping.Load() {
		return false
	}

	select {
	case p.events <- eventID:
		return true
	default:
		if p.log != nil {
			p.log.Warn("deferred intake queue full; leaving event durable for replay",
				"event_id", eventID,
				"queue_depth", len(p.events),
				"queue_limit", cap(p.events),
			)
		}
		return false
	}
}

func (p *deferredProcessor) Close() {
	if p == nil {
		return
	}
	if p.stopping.Swap(true) {
		return
	}
	close(p.done)
	p.wg.Wait()
}

func (p *deferredProcessor) worker(ctx context.Context) {
	for {
		select {
		case eventID := <-p.events:
			p.processEvent(ctx, eventID)
		case <-p.done:
			return
		}
	}
}

func (p *deferredProcessor) processEvent(ctx context.Context, eventID string) {
	record, err := p.store.Get(ctx, eventID)
	if err != nil {
		if p.log != nil {
			p.log.WarnContext(ctx, "load deferred intake failed", "event_id", eventID, "err", err)
		}
		return
	}
	if record.DeferredState != intake.DeferredStatePending {
		return
	}
	p.processRecord(ctx, record)
}

func (p *deferredProcessor) processRecord(ctx context.Context, record intake.Record) {
	deferredEvent, ok := p.rebuildDeferredAudit(ctx, record)
	if !ok {
		return
	}
	if p.sink != nil {
		hook.WriteDeferredAudit(ctx, deferredEvent, p.sink)
	}
	if err := p.store.MarkDeferredComplete(ctx, record.EventID); err != nil && p.log != nil {
		p.log.WarnContext(ctx, "mark deferred intake complete failed", "event_id", record.EventID, "err", err)
	}
}

func (p *deferredProcessor) rebuildDeferredAudit(ctx context.Context, record intake.Record) (hook.DeferredAuditEvent, bool) {
	getenv := func(key string) string {
		return record.EnvFingerprint[key]
	}
	hint := hook.SystemFromString(record.System)

	syncCfg := hook.SyncConfig(p.cfg)
	syncEval := hook.EvaluateHot(ctx, record.RawPayload, syncCfg, hint, getenv)
	if !syncEval.Deferred.Valid {
		if p.log != nil {
			p.log.WarnContext(ctx, "replay sync evaluation produced invalid deferred event", "event_id", record.EventID)
		}
		var empty hook.DeferredAuditEvent
		return empty, false
	}

	merged := syncEval.Deferred
	syncRules, deferredRules := hook.PartitionRules(p.cfg)
	merged.Rules = append(append([]config.Rule(nil), syncRules...), deferredRules...)

	if len(deferredRules) > 0 {
		deferredCfg := hook.DeferredConfig(p.cfg)
		deferredEval := hook.EvaluateHot(ctx, record.RawPayload, deferredCfg, hint, getenv)
		if deferredEval.Deferred.Valid {
			merged.AuditOnlyViolations = deferredEval.Deferred.AuditOnlyViolations
		} else if p.log != nil {
			p.log.WarnContext(ctx, "replay deferred evaluation produced invalid deferred event", "event_id", record.EventID)
		}
	}
	return merged, true
}
