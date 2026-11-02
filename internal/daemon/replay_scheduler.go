package daemon

import (
	"context"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/logstorage"
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
	s.auditRequested = true
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		return
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
		if reconcile {
			if err := logstorage.Maintain(config.DefaultStateDir()); err != nil {
				s.log.WarnContext(ctx, "maintain local logs failed", "err", err)
			}
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
	s.lockLifecycle()
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
		full, err := dispatchDueBucket(ctx, store, s.deferredProcessor, handle.Bucket, now)
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
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.stopping.Load() {
		return false
	}
	if _, exists := p.queued[key]; exists {
		return true
	}
	if p.service.paused || len(p.events) >= p.service.queueLimit {
		return false
	}
	if p.queuedBytes+size > replayDispatchBytes {
		return false
	}
	select {
	case p.events <- deferredWork{receiptID: receiptID, bucket: bucket, epoch: p.service.epoch}:
		p.queued[key] = queuedReplay{size: size, epoch: p.service.epoch}
		p.queuedBytes += size
		return true
	default:
		return false
	}
}

func (p *deferredProcessor) finishDispatch(work deferredWork) {
	key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
	p.queueMu.Lock()
	if queued, exists := p.queued[key]; exists && queued.epoch == work.epoch {
		p.queuedBytes -= queued.size
		delete(p.queued, key)
	}
	p.queueMu.Unlock()
	if p.notify != nil {
		p.notify()
	}
}

func (p *deferredProcessor) processDispatch(ctx context.Context, work deferredWork) {
	if ctx.Err() != nil {
		return
	}
	policy := p.cfg.AuditStoragePolicy()
	if policy.RetentionBuckets > 0 {
		expires := work.bucket.Start.Add(time.Duration(policy.RetentionBuckets) * policy.BucketInterval)
		remaining := expires.Sub(p.now())
		if remaining <= 0 {
			return
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}
	p.processEvent(ctx, work)
}
