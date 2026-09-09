package daemon

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/intake"
)

const (
	replayPageLimit     = 100
	replayDispatchBytes = 1024 * 1024
	replayMetadataBytes = 128
)

// ReplayKey identifies a receipt within its disposable database.
type ReplayKey struct {
	BucketID  string
	ReceiptID int64
}

// StartAuditScheduler starts the single rotation, expiration, and replay owner after readiness.
func (s *Server) StartAuditScheduler(ctx context.Context) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.closing {
		return
	}
	select {
	case <-s.shutdown:
		return
	default:
	}
	s.auditOnce.Do(func() {
		workerContext, cancel := context.WithCancel(ctx)
		s.auditCancel = cancel
		s.auditWG.Go(func() {
			defer cancel()
			s.auditScheduler(workerContext)
		})
	})
}

func (s *Server) wakeAuditScheduler() {
	select {
	case s.auditWake <- struct{}{}:
	default:
	}
}

func (s *Server) auditScheduler(ctx context.Context) {
	ticks := s.auditTicks
	if ticks == nil {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		ticks = ticker.C
	}
	var retryAt time.Time
	attempt := 0
	started := false
	now := s.now()
	reconcile := true
	for {
		if ctx.Err() != nil {
			return
		}
		if reconcile && !now.Before(retryAt) {
			if err := s.reconcileAuditStorage(ctx, now); err != nil {
				attempt++
				retryAt = now.Add(auditRetryDelay(attempt))
				s.log.WarnContext(ctx, "audit reconciliation failed; retrying", "err", err, "retry_at", retryAt)
			} else {
				attempt = 0
				retryAt = time.Time{}
			}
		}
		reconcile = false
		if err := s.dispatchRetainedReplay(ctx, now); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "retained audit replay scan failed", "err", err)
		}
		if !started {
			close(s.auditStarted)
			started = true
		}
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			now = s.now()
			reconcile = true
		case <-s.auditWake:
		}
	}
}

func (s *Server) dispatchRetainedReplay(ctx context.Context, now time.Time) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() || s.closing {
		return nil
	}
	policy := snapshot.cfg.AuditStoragePolicy()
	readers, err := s.catalog.Read(ctx, policy.Rotation(), now)
	if err != nil {
		return wrapServerError("access retained audit storage", err)
	}
	defer func() { _ = readers.Close() }()
	for _, handle := range readers.Handles {
		store, err := intake.NewStore(ctx, handle.Database, policy, s.log)
		if err != nil {
			return wrapServerError("access retained audit storage", err)
		}
		full, err := dispatchDueBucket(ctx, store, snapshot.deferredProcessor, handle.Bucket, now)
		if err != nil {
			return wrapServerError("access retained audit storage", err)
		}
		if full {
			return nil
		}
	}
	return nil
}

func dispatchDueBucket(ctx context.Context, store *intake.Store, processor *deferredProcessor, bucket auditstorage.Bucket, now time.Time) (bool, error) {
	var after int64
	for {
		if err := ctx.Err(); err != nil {
			return false, wrapServerError("dispatch deferred receipts", err)
		}
		identifiers, err := store.ListDueDeferred(ctx, now, after, replayPageLimit)
		if err != nil {
			return false, wrapServerError("dispatch deferred receipts", err)
		}
		for _, identifier := range identifiers {
			if !processor.dispatch(bucket, identifier) {
				return true, nil
			}
			after = identifier
		}
		if len(identifiers) < replayPageLimit {
			return false, nil
		}
	}
}

func (p *deferredProcessor) dispatch(bucket auditstorage.Bucket, receiptID int64) bool {
	key := ReplayKey{BucketID: bucket.ID, ReceiptID: receiptID}
	size := replayMetadataBytes + len(bucket.ID) + len(bucket.Path)
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.stopping.Load() {
		return false
	}
	if _, exists := p.queued[key]; exists {
		return true
	}
	if p.queuedBytes+size > replayDispatchBytes {
		return false
	}
	select {
	case p.events <- deferredWork{receiptID: receiptID, bucket: bucket}:
		p.queued[key] = size
		p.queuedBytes += size
		return true
	default:
		return false
	}
}

func (p *deferredProcessor) finishDispatch(work deferredWork) {
	key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
	p.queueMu.Lock()
	p.queuedBytes -= p.queued[key]
	delete(p.queued, key)
	p.queueMu.Unlock()
	if p.notify != nil {
		p.notify()
	}
}

func (p *deferredProcessor) processDispatch(ctx context.Context, work deferredWork) {
	defer p.finishDispatch(work)
	expires := work.bucket.Start.Add(time.Duration(p.cfg.AuditStoragePolicy().RetentionBuckets) * p.cfg.AuditStoragePolicy().BucketInterval)
	remaining := expires.Sub(p.now())
	if remaining <= 0 || ctx.Err() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, remaining)
	defer cancel()
	if work.bucket.ID == p.bucket.ID {
		p.processEvent(ctx, work)
		return
	}
	if err := p.processRetained(ctx, work); err != nil && ctx.Err() == nil {
		p.log.WarnContext(ctx, "process retained audit receipt failed", "bucket", work.bucket.ID, "receipt_id", work.receiptID, "err", err)
	}
}

func (p *deferredProcessor) processRetained(ctx context.Context, work deferredWork) error {
	handle, err := p.catalog.OpenWriter(ctx, work.bucket)
	if err != nil {
		return wrapServerError("access retained audit storage", err)
	}
	defer func() { _ = handle.Close() }()
	store, err := intake.NewStore(ctx, handle.Database, p.cfg.AuditStoragePolicy(), p.log)
	if err != nil {
		return wrapServerError("access retained audit storage", err)
	}
	logger, err := audit.NewEventLoggerWithOptions(ctx, p.cfg, p.log, audit.LoggerOptions{QueueLimit: 0, BatchMaxItems: 0, BatchMaxBytes: 0, QueueMaxBytes: 0, SharedDB: handle.Database})
	if err != nil {
		p.log.WarnContext(ctx, "create retained audit logger failed", "err", err)
		return fmt.Errorf("create retained audit logger: %w", err)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := logger.CloseContext(closeContext); err != nil {
			p.log.WarnContext(ctx, "close retained audit logger failed", "err", err)
		}
	}()
	adapter := &sqliteIntakeStore{store: store, log: p.log}
	processor := newDeferredProcessor(ctx, adapter, audit.NewLocalSink(logger), p.cfg, p.inferRuntime, 1, 0, p.log)
	processor.evaluationRecorder = adapter.Evaluations()
	processor.bucket = work.bucket
	processor.now = p.now
	defer processor.Close()
	processor.processEvent(ctx, work)
	return nil
}
