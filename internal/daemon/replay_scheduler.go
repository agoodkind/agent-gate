package daemon

import (
	"context"
	"slices"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
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
	var maintenance auditMaintenance

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
			err := maintenance.run(ctx, s, now)
			if err != nil && ctx.Err() == nil {
				attempt++
				retryAt = now.Add(auditRetryDelay(attempt))
				s.log.WarnContext(
					ctx,
					"audit reconciliation failed; retrying",
					"err",
					err,
					"retry_at",
					retryAt,
				)
			} else {
				attempt = 0
				retryAt = time.Time{}
			}
		}

		reconcile = false
		if !started {
			_ = s.dispatchRetainedReplay(ctx, now)
			close(s.auditStarted)
			started = true
		}

		s.deferredProcessor.recoveryTick(now)
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
	if err := ctx.Err(); err != nil {
		return wrapServerError("request retained recovery", err)
	}

	p := s.deferredProcessor
	p.service.mu.Lock()
	p.service.recovery.ready = true
	p.service.recovery.historyGeneration++
	p.service.mu.Unlock()
	p.recoveryTick(now)

	return nil
}

func (p *deferredProcessor) submit(bucket auditstorage.Bucket, receiptID int64) {
	if !p.dispatch(bucket, receiptID) {
		p.service.mu.Lock()
		p.markDiscoveryLocked(bucket)
		p.service.mu.Unlock()
	}

	p.wakeWorkers()
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

	if p.service.paused || p.service.recovery.creditReserved ||
		slices.Contains(p.service.excluded, bucket.Path) ||
		len(p.events) >= p.service.queueLimit {
		return false
	}

	if p.queuedBytes+size > replayDispatchBytes {
		return false
	}

	select {
	case p.events <- deferredWork{
		receiptID: receiptID,
		bucket:    bucket,
		epoch:     p.service.epoch,
		probe:     false,
	}:
		p.queued[key] = queuedReplay{
			size:     size,
			epoch:    p.service.epoch,
			revisit:  false,
			complete: false,
		}
		p.queuedBytes += size

		return true
	default:
		return false
	}
}

func (p *deferredProcessor) finishDispatch(work deferredWork) {
	key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
	p.service.mu.Lock()
	p.queueMu.Lock()
	if queued, exists := p.queued[key]; exists && queued.epoch == work.epoch {
		if queued.revisit && !queued.complete {
			p.markDiscoveryLocked(work.bucket)
		}

		p.queuedBytes -= queued.size
		delete(p.queued, key)
		p.service.recovery.capacityBlocked = false
	}

	p.queueMu.Unlock()
	p.service.mu.Unlock()
	if p.notify != nil {
		p.notify()
	}
}

func (p *deferredProcessor) processDispatch(ctx context.Context, work deferredWork) bool {
	if ctx.Err() != nil {
		return false
	}

	policy := p.cfg.AuditStoragePolicy()
	if policy.RetentionBuckets > 0 {
		expires := work.bucket.Start.Add(
			time.Duration(policy.RetentionBuckets) * policy.BucketInterval,
		)
		remaining := expires.Sub(p.now())
		if remaining <= 0 {
			return false
		}

		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, remaining)
		defer cancel()
	}

	return p.processEvent(ctx, work)
}
