package daemon

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

type activeDeferredWork struct {
	path    string
	bucket  auditstorage.Bucket
	cancel  context.CancelFunc
	session *recoverySession
}

type deferredService struct {
	mu           sync.Mutex
	wake         chan struct{}
	workers      int
	queueLimit   int
	paused       bool
	next         uint64
	epoch        uint64
	active       map[uint64]activeDeferredWork
	jobs         sync.WaitGroup
	server       *Server
	recovery     recoveryState
	excluded     []string
	transition   bool
	cacheVersion uint64
}

func (p *deferredProcessor) configure(cfg *config.Config) {
	p.service.mu.Lock()
	p.queueMu.Lock()
	capacity := deferredQueueCapacity(cfg.HookDeferredQueueLimit())
	if capacity > cap(p.events) {
		events := make(chan deferredWork, capacity)
		for _, work := range drainDeferredWork(p.events) {
			events <- work
		}

		p.events = events
	}

	p.queueMu.Unlock()
	p.service.workers = cfg.HookDeferredWorkers()
	p.service.queueLimit = cfg.HookDeferredQueueLimit()
	p.service.paused = false
	p.service.transition = false
	if len(p.service.excluded) != 0 {
		p.service.excluded = nil
		p.service.recovery.historyGeneration++
	}

	p.storageChangedLocked()
	p.service.mu.Unlock()
	p.wakeWorkers()
}

func deferredQueueCapacity(limit int) int {
	return min(limit, replayDispatchBytes/replayMetadataBytes)
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
		var work deferredWork
		admitted := false
		if !p.service.paused && (p.service.server == nil || p.service.recovery.ready) &&
			len(p.service.active) < p.service.workers {
			work, admitted = p.takeWorkLocked()
		}

		if admitted && ctx.Err() == nil {
			jobContext, cancel := context.WithCancel(ctx)
			p.service.next++
			identifier := p.service.next
			p.service.active[identifier] = activeDeferredWork{
				path:    work.bucket.Path,
				bucket:  work.bucket,
				cancel:  cancel,
				session: nil,
			}
			p.service.jobs.Add(1)
			p.service.mu.Unlock()
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						p.log.ErrorContext(
							jobContext,
							"deferred intake cleanup panic recovered",
							"err",
							recovered,
						)
					}
				}()
				p.runWork(jobContext, work, identifier)
			}()
			continue
		}

		p.service.mu.Unlock()
		select {
		case <-p.service.wake:
		case <-ctx.Done():
			return
		case <-p.done:
			return
		}
	}
}

func (p *deferredProcessor) takeWorkLocked() (deferredWork, bool) {
	var work deferredWork
	recovery := &p.service.recovery
	if recovery.ready && !p.service.transition && !recovery.probing && recovery.required() &&
		(recovery.turn || len(p.events) == 0) {
		work.epoch = p.service.epoch
		work.probe = true
		recovery.probing = true
		recovery.turn = false

		return work, true
	}

	for range len(p.events) {
		work = <-p.events
		if p.service.transition && !p.currentFile(work.bucket) {
			p.events <- work
			continue
		}

		recovery.turn = true

		return work, true
	}

	return work, false
}

func (p *deferredProcessor) runWork(ctx context.Context, work deferredWork, identifier uint64) {
	var session recoverySession
	p.service.mu.Lock()
	active := p.service.active[identifier]
	active.session = &session
	p.service.active[identifier] = active
	p.service.mu.Unlock()
	defer func() {
		session.close(p, ctx)
		p.service.mu.Lock()
		p.service.active[identifier].cancel()
		delete(p.service.active, identifier)
		p.service.mu.Unlock()
		p.service.jobs.Done()
		p.wakeWorkers()
	}()

	for {
		p.runSessionWork(ctx, work, identifier, &session)
		p.service.mu.Lock()
		if ctx.Err() != nil || p.service.paused || p.stopping.Load() ||
			len(p.service.active) > p.service.workers {
			p.service.mu.Unlock()
			return
		}

		next, available := p.takeWorkLocked()
		active := p.service.active[identifier]
		active.path = next.bucket.Path
		active.bucket = next.bucket
		p.service.active[identifier] = active
		p.service.mu.Unlock()
		if !available {
			return
		}

		work = next
	}
}

func (p *deferredProcessor) runSessionWork(
	ctx context.Context,
	work deferredWork,
	identifier uint64,
	session *recoverySession,
) {
	probing := work.probe
	settled := false
	defer func() {
		p.service.mu.Lock()
		if probing {
			p.service.recovery.probing = false
			p.service.recovery.historyGeneration++
		}

		if !settled && work.receiptID != 0 {
			p.markDiscoveryLocked(work.bucket)
		}

		p.service.mu.Unlock()
		p.finishDispatch(work)
		p.wakeWorkers()
	}()
	var owner *fileOwner
	if work.probe {
		work, owner = p.recoverWork(ctx, identifier, session)
		p.service.mu.Lock()
		p.service.recovery.probing = false
		probing = false
		p.service.mu.Unlock()
		p.wakeWorkers()
		if work.receiptID == 0 {
			return
		}
	}

	if p.service.server == nil {
		p.processDispatch(ctx, work)
		settled = true

		return
	}

	if !work.probe && owner == nil {
		snapshot := p.service.server.runtime.Load()
		if snapshot == nil ||
			!snapshot.cfg.AuditStoragePolicy().Rotation().Retains(work.bucket, p.now()) {
			settled = true
			return
		}

		session.prepare(p, ctx, work.bucket)
	}

	job, release, err := prepareDeferredJob(ctx, p.service.server, work, owner)
	if err != nil {
		if !recoveryCancellation(ctx, err) {
			p.log.WarnContext(
				ctx,
				"prepare deferred job failed",
				"bucket",
				work.bucket.ID,
				"err",
				err,
			)
		}

		p.deferDiscovery(work.bucket, p.now().Add(time.Second))
		settled = true

		return
	}

	if job == nil {
		return
	}

	defer release()
	completed := job.processDispatch(ctx, work)
	finalizeRelease := func() { release(); session.closeFile(p, ctx, work.bucket) }
	p.finishJob(ctx, job, work, finalizeRelease, completed)
	settled = true
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
	p.service.mu.Lock()
	p.service.recovery.reset()
	p.service.mu.Unlock()
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
	p.service.mu.Lock()
	for _, path := range p.service.excluded {
		if !slices.Contains(paths, path) {
			p.service.recovery.historyGeneration++
			break
		}
	}

	p.service.excluded = slices.Clone(paths)
	for _, work := range p.service.active {
		if slices.Contains(paths, work.path) {
			work.cancel()
		}
	}

	p.queueMu.Lock()

	remaining := make([]deferredWork, 0, len(p.events))
	for _, work := range drainDeferredWork(p.events) {
		key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
		queued := p.queued[key]
		if slices.Contains(paths, work.bucket.Path) {
			p.queuedBytes -= queued.size
			delete(p.queued, key)
			p.service.recovery.capacityBlocked = false
			continue
		}

		remaining = append(remaining, work)
	}

	for _, work := range remaining {
		p.events <- work
	}

	p.queueMu.Unlock()
	p.service.mu.Unlock()
	p.wakeWorkers()
}

func drainDeferredWork(events <-chan deferredWork) []deferredWork {
	work := make([]deferredWork, 0, len(events))

	for {
		select {
		case next := <-events:
			work = append(work, next)
		default:
			return work
		}
	}
}

func prepareDeferredJob(
	ctx context.Context,
	s *Server,
	work deferredWork,
	owner *fileOwner,
) (*deferredProcessor, func(), error) {
	s.runtimeMu.RLock()
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		s.runtimeMu.RUnlock()
		if owner != nil {
			if err := owner.release(); err != nil {
				return nil, nil, wrapServerError("release canceled recovery file", err)
			}
		}

		return nil, nil, nil
	}

	cfg := snapshot.cfg
	catalog := snapshot.catalog
	p := s.deferredProcessor
	p.service.mu.Lock()
	delayed := p.service.transition && work.bucket.Path != snapshot.bucket.Path && owner == nil
	p.service.mu.Unlock()
	if delayed {
		s.runtimeMu.RUnlock()
		return nil, nil, nil
	}

	inference := snapshot.inference
	inference.references.Add(1)
	var store intakeStore
	var recorder evaluationRecorder
	var err error
	if work.bucket.Path == snapshot.bucket.Path {
		if owner == nil {
			owner, err = s.owners.borrow(ctx, catalog, work.bucket)
		}

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

	release := deferredJobRelease(ctx, s, owner, inference)
	if store == nil {
		retained, err := intake.NewStore(
			ctx,
			owner.handle.Database,
			cfg.AuditStoragePolicy(),
			s.log,
		)
		if err != nil {
			release()
			return nil, nil, wrapServerError("prepare deferred store", err)
		}

		adapter := &sqliteIntakeStore{store: retained, log: s.log}
		store = adapter
		recorder = adapter.Evaluations()
	}

	return &deferredProcessor{
		catalog:            catalog,
		service:            nil,
		events:             nil,
		done:               nil,
		wg:                 sync.WaitGroup{},
		stopping:           atomic.Bool{},
		cancel:             nil,
		notify:             nil,
		queueMu:            sync.Mutex{},
		queued:             nil,
		queuedBytes:        0,
		store:              store,
		evaluationRecorder: recorder,
		sink:               audit.NewNormalizer(cfg),
		cfg:                cfg,
		inferRuntime:       inference.runtime,
		log:                s.log,
		claimOwner:         s.deferredProcessor.claimOwner,
		claimLease:         cfg.DeferredClaimLease(),
		claimRenewInterval: cfg.DeferredClaimRenewInterval(),
		bucket:             work.bucket,
		now:                s.now,
	}, release, nil
}

func deferredJobRelease(
	ctx context.Context,
	server *Server,
	owner *fileOwner,
	inference *inferenceOwner,
) func() {
	return sync.OnceFunc(func() {
		if err := owner.release(); err != nil {
			server.log.WarnContext(ctx, "release deferred file failed", "err", err)
		}

		inference.release()
	})
}
