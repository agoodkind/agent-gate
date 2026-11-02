package daemon

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

type activeDeferredWork struct {
	path   string
	cancel context.CancelFunc
}

type deferredService struct {
	mu         sync.Mutex
	wake       chan struct{}
	workers    int
	queueLimit int
	paused     bool
	next       uint64
	epoch      uint64
	active     map[uint64]activeDeferredWork
	jobs       sync.WaitGroup
	server     *Server
}

func (p *deferredProcessor) configure(cfg *config.Config) {
	p.service.mu.Lock()
	p.queueMu.Lock()
	if cfg.HookDeferredQueueLimit() > cap(p.events) {
		events := make(chan deferredWork, cfg.HookDeferredQueueLimit())
		for len(p.events) > 0 {
			events <- <-p.events
		}
		p.events = events
	}
	p.queueMu.Unlock()
	p.service.workers = cfg.HookDeferredWorkers()
	p.service.queueLimit = cfg.HookDeferredQueueLimit()
	p.service.paused = false
	p.service.mu.Unlock()
	p.wakeWorkers()
}

func (p *deferredProcessor) wakeWorkers() {
	select {
	case p.service.wake <- struct{}{}:
	default:
	}
}

func (p *deferredProcessor) serve(ctx context.Context) {
	for {
		p.service.mu.Lock()
		var events <-chan deferredWork
		if !p.service.paused && len(p.service.active) < p.service.workers {
			events = p.events
		}
		p.service.mu.Unlock()
		select {
		case work := <-events:
			p.service.mu.Lock()
			// Recheck admission after a concurrent worker reduction or pause.
			if p.service.paused || work.epoch != p.service.epoch || len(p.service.active) >= p.service.workers || ctx.Err() != nil {
				p.service.mu.Unlock()
				p.finishDispatch(work)
				continue
			}
			jobContext, cancel := context.WithCancel(ctx)
			p.service.next++
			identifier := p.service.next
			p.service.active[identifier] = activeDeferredWork{path: work.bucket.Path, cancel: cancel}
			p.service.jobs.Add(1)
			p.service.mu.Unlock()
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						p.log.ErrorContext(jobContext, "deferred intake cleanup panic recovered", "err", recovered)
					}
				}()
				p.runWork(jobContext, work, identifier)
			}()
		case <-p.service.wake:
		case <-ctx.Done():
			return
		case <-p.done:
			return
		}
	}
}

func (p *deferredProcessor) runWork(ctx context.Context, work deferredWork, identifier uint64) {
	defer func() {
		p.service.mu.Lock()
		p.service.active[identifier].cancel()
		delete(p.service.active, identifier)
		p.service.mu.Unlock()
		p.finishDispatch(work)
		p.service.jobs.Done()
		p.wakeWorkers()
	}()
	if p.service.server == nil {
		p.processDispatch(ctx, work)
		return
	}
	job, release, err := prepareDeferredJob(ctx, p.service.server, work)
	if err != nil {
		if ctx.Err() == nil {
			p.log.WarnContext(ctx, "prepare deferred job failed", "bucket", work.bucket.ID, "err", err)
		}
		return
	}
	if job == nil {
		return
	}
	defer release()
	job.processDispatch(ctx, work)
}

// pauseAndDrain cancels jobs before a storage reset invalidates their files.
func (p *deferredProcessor) pauseAndDrain() {
	p.service.mu.Lock()
	p.service.paused = true
	p.service.epoch++
	for _, work := range p.service.active {
		work.cancel()
	}
	p.service.mu.Unlock()
	p.wakeWorkers()
	p.service.jobs.Wait()
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	for {
		select {
		case <-p.events:
		default:
			clear(p.queued)
			p.queuedBytes = 0
			return
		}
	}
}

func (p *deferredProcessor) cancelFiles(paths []string) {
	if len(paths) == 0 {
		return
	}
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	p.service.epoch++
	for _, work := range p.service.active {
		if slices.Contains(paths, work.path) {
			work.cancel()
		}
	}
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	remaining := make([]deferredWork, 0, len(p.events))
	for len(p.events) > 0 {
		work := <-p.events
		key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
		queued := p.queued[key]
		if slices.Contains(paths, work.bucket.Path) {
			p.queuedBytes -= queued.size
			delete(p.queued, key)
			continue
		}
		work.epoch = p.service.epoch
		queued.epoch = p.service.epoch
		p.queued[key] = queued
		remaining = append(remaining, work)
	}
	for _, work := range remaining {
		p.events <- work
	}
}

func prepareDeferredJob(ctx context.Context, s *Server, work deferredWork) (*deferredProcessor, func(), error) {
	s.runtimeMu.RLock()
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		s.runtimeMu.RUnlock()
		return nil, nil, nil
	}
	cfg := snapshot.cfg
	catalog := snapshot.catalog
	inference := snapshot.inference
	inference.references.Add(1)
	var owner *fileOwner
	var store intakeStore
	var recorder evaluationRecorder
	var err error
	if work.bucket.Path == snapshot.bucket.Path {
		owner, err = s.owners.borrow(ctx, catalog, work.bucket)
		store = snapshot.intakeStore
		recorder = snapshot.evaluationRecorder
	}
	s.runtimeMu.RUnlock()
	if err != nil {
		inference.release()
		return nil, nil, wrapServerError("prepare deferred file", err)
	}
	if owner == nil {
		owner, err = s.owners.borrow(ctx, catalog, work.bucket)
		if err != nil {
			inference.release()
			return nil, nil, wrapServerError("prepare deferred file", err)
		}
	}
	release := func() {
		if err := owner.release(); err != nil {
			s.log.WarnContext(ctx, "release deferred file failed", "err", err)
		}
		inference.release()
	}
	if store == nil {
		retained, err := intake.NewStore(ctx, owner.handle.Database, cfg.AuditStoragePolicy(), s.log)
		if err != nil {
			release()
			return nil, nil, wrapServerError("prepare deferred store", err)
		}
		adapter := &sqliteIntakeStore{store: retained, log: s.log}
		store = adapter
		recorder = adapter.Evaluations()
	}
	var sink audit.Sink
	logger, err := audit.NewEventLoggerWithOptions(ctx, cfg, s.log, audit.LoggerOptions{
		QueueLimit: 0, BatchMaxItems: 0, BatchMaxBytes: 0, QueueMaxBytes: 0, SharedDB: owner.handle.Database,
	})
	if err != nil {
		release()
		return nil, nil, wrapServerError("prepare deferred audit logger", err)
	}
	if logger.Enabled() {
		sink = audit.NewLocalSink(logger)
	}
	releaseJob := func() {
		closeContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := logger.CloseContext(closeContext); err != nil {
			s.log.WarnContext(closeContext, "close deferred audit logger failed", "err", err)
		}
		release()
	}
	return &deferredProcessor{
		service: nil, events: nil, done: nil, wg: sync.WaitGroup{},
		stopping: atomic.Bool{}, cancel: nil,
		notify: nil, queueMu: sync.Mutex{}, queued: nil, queuedBytes: 0,
		store: store, evaluationRecorder: recorder, sink: sink,
		cfg: cfg, inferRuntime: inference.runtime, log: s.log,
		claimOwner:         s.deferredProcessor.claimOwner,
		claimLease:         cfg.DeferredClaimLease(),
		claimRenewInterval: cfg.DeferredClaimRenewInterval(),
		bucket:             work.bucket, now: s.now,
	}, releaseJob, nil
}
