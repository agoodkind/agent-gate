package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/intake"
)

type recoveryState struct {
	ready               bool
	probing             bool
	turn                bool
	capacityBlocked     bool
	creditReserved      bool
	currentPath         string
	currentGeneration   uint64
	currentAcknowledged uint64
	historyGeneration   uint64
	historyAcknowledged uint64
	currentDeadline     time.Time
	historyDeadline     time.Time
	lastClock           time.Time
	clockGeneration     uint64
	pass                *recoveryPass
}

type recoveryPass struct {
	historical        bool
	currentGeneration uint64
	historyGeneration uint64
	clockGeneration   uint64
	epoch             uint64
	at                time.Time
	files             *auditstorage.RecoveryFiles
	buckets           []auditstorage.Bucket
	bucket            auditstorage.Bucket
	cursor            *intake.PendingCursor
	currentDone       bool
}

func (recovery *recoveryState) required() bool {
	return !recovery.capacityBlocked && recovery.pending()
}

func (recovery *recoveryState) pending() bool {
	return recovery.pass != nil || recovery.currentGeneration != recovery.currentAcknowledged ||
		recovery.historyGeneration != recovery.historyAcknowledged
}

func (recovery *recoveryState) reset() {
	if recovery.pass != nil && recovery.pass.files != nil {
		_ = recovery.pass.files.Close()
	}

	recovery.pass = nil
	recovery.capacityBlocked = false
	recovery.creditReserved = false
	recovery.currentGeneration++
	recovery.historyGeneration++
}

func earlierDeadline(destination *time.Time, candidate time.Time) {
	if !candidate.IsZero() && (destination.IsZero() || candidate.Before(*destination)) {
		*destination = candidate
	}
}

func (p *deferredProcessor) currentFile(bucket auditstorage.Bucket) bool {
	if p.service.server == nil {
		return bucket.Path == p.bucket.Path
	}

	snapshot := p.service.server.runtime.Load()

	return snapshot != nil && snapshot.bucket.Path == bucket.Path
}

func (p *deferredProcessor) markDiscoveryLocked(bucket auditstorage.Bucket) {
	if p.currentFile(bucket) {
		p.service.recovery.currentGeneration++
	} else {
		p.service.recovery.historyGeneration++
	}
}

func (p *deferredProcessor) storageChangedLocked() {
	if p.service.server == nil {
		return
	}

	snapshot := p.service.server.runtime.Load()
	if snapshot == nil || snapshot.bucket.Path == p.service.recovery.currentPath {
		return
	}

	recovery := &p.service.recovery
	recovery.currentPath = snapshot.bucket.Path
	recovery.currentGeneration++
	recovery.historyGeneration++
	earlierDeadline(&recovery.historyDeadline, recovery.currentDeadline)
	recovery.currentDeadline = time.Time{}
}

func (p *deferredProcessor) deferDiscovery(bucket auditstorage.Bucket, at time.Time) {
	at = at.Round(0)
	p.service.mu.Lock()
	if p.currentFile(bucket) {
		earlierDeadline(&p.service.recovery.currentDeadline, at)
	} else {
		earlierDeadline(&p.service.recovery.historyDeadline, at)
	}

	p.service.mu.Unlock()
}

func (p *deferredProcessor) recoveryTick(now time.Time) {
	// SQLite eligibility uses wall time, including clock adjustments.
	now = now.Round(0)
	p.service.mu.Lock()
	recovery := &p.service.recovery
	if now.Before(recovery.lastClock.Round(0)) {
		recovery.clockGeneration++
		recovery.currentGeneration++
		recovery.historyGeneration++
	}

	recovery.lastClock = now
	if !recovery.currentDeadline.IsZero() && !now.Before(recovery.currentDeadline) {
		recovery.currentGeneration++
		recovery.currentDeadline = time.Time{}
	}

	if !recovery.historyDeadline.IsZero() && !now.Before(recovery.historyDeadline) {
		recovery.historyGeneration++
		recovery.historyDeadline = time.Time{}
	}

	p.service.mu.Unlock()
	p.wakeWorkers()
}

func (p *deferredProcessor) recoveryPass() *recoveryPass {
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	recovery := &p.service.recovery
	if recovery.pass != nil &&
		(recovery.pass.epoch != p.service.epoch || recovery.pass.clockGeneration != recovery.clockGeneration) {
		recovery.reset()
	}

	if recovery.pass == nil {
		var bucket auditstorage.Bucket
		recovery.pass = &recoveryPass{
			historical:        recovery.historyGeneration != recovery.historyAcknowledged,
			currentGeneration: recovery.currentGeneration,
			historyGeneration: recovery.historyGeneration,
			clockGeneration:   recovery.clockGeneration,
			epoch:             p.service.epoch,
			at:                p.now().Round(0),
			files:             nil,
			buckets:           nil,
			bucket:            bucket,
			cursor:            nil,
			currentDone:       false,
		}
	}

	return recovery.pass
}

func (pass *recoveryPass) clearFile() {
	var bucket auditstorage.Bucket
	pass.bucket = bucket
	pass.cursor = nil
}

func (p *deferredProcessor) acknowledgePass(pass *recoveryPass) {
	if pass.files != nil {
		_ = pass.files.Close()
	}

	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	recovery := &p.service.recovery
	if pass.epoch == p.service.epoch && pass.clockGeneration == recovery.clockGeneration {
		if pass.currentGeneration == recovery.currentGeneration {
			recovery.currentAcknowledged = pass.currentGeneration
		}

		if pass.historical && pass.historyGeneration == recovery.historyGeneration {
			recovery.historyAcknowledged = pass.historyGeneration
		}
	}

	recovery.pass = nil
	recovery.creditReserved = false
}

// recoverWork examines one bounded page within an existing worker slot.
func (p *deferredProcessor) recoverWork(
	ctx context.Context,
	identifier uint64,
	session *recoverySession,
) (deferredWork, *fileOwner) {
	var empty deferredWork
	pass := p.recoveryPass()
	s := p.service.server
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		p.acknowledgePass(pass)
		return empty, nil
	}

	if !p.selectRecoveryFile(ctx, pass, snapshot) {
		return empty, nil
	}

	bucket := pass.bucket
	p.service.mu.Lock()
	active := p.service.active[identifier]
	active.path = bucket.Path
	active.bucket = bucket
	p.service.active[identifier] = active
	valid := pass.epoch == p.service.epoch && !p.service.paused
	excluded := slices.Contains(p.service.excluded, bucket.Path)
	p.service.mu.Unlock()
	if excluded || !snapshot.cfg.AuditStoragePolicy().Rotation().Retains(bucket, p.now()) {
		pass.clearFile()
		return empty, nil
	}

	if !valid || ctx.Err() != nil {
		return empty, nil
	}

	complete, err := auditstorage.Completed(bucket.Path)
	if err != nil {
		p.recoveryError(ctx, pass, bucket, err)
		return empty, nil
	}

	if complete {
		pass.clearFile()
		return empty, nil
	}

	owner, err := session.borrow(ctx, p, snapshot.catalog, bucket)
	if err != nil {
		if errors.Is(err, errRecoveryTransition) {
			return empty, nil
		}

		if errors.Is(err, auditstorage.ErrFileComplete) {
			pass.clearFile()
			return empty, nil
		}

		p.recoveryError(ctx, pass, bucket, err)

		return empty, nil
	}

	transferred := false
	defer func() {
		if !transferred {
			if err := owner.release(); err != nil {
				p.log.WarnContext(
					ctx,
					"release recovery file failed",
					"bucket",
					bucket.ID,
					"err",
					err,
				)
			}
		}
	}()
	store, err := intake.NewStore(
		ctx,
		owner.handle.Database,
		snapshot.cfg.AuditStoragePolicy(),
		s.log,
	)
	if err != nil {
		p.recoveryError(ctx, pass, bucket, err)
		return empty, nil
	}

	work, finished, err := p.recoverPage(ctx, pass, store)
	if err != nil {
		p.recoveryError(ctx, pass, bucket, err)
		return empty, nil
	}

	if work.receiptID != 0 {
		transferred = true
		return work, owner
	}

	if finished {
		pass.clearFile()
		transferred = p.finishRecoveredFile(ctx, store, owner, session, snapshot.catalog)
	}

	return work, nil
}

func (p *deferredProcessor) finishRecoveredFile(
	ctx context.Context,
	store *intake.Store,
	owner *fileOwner,
	session *recoverySession,
	catalog *auditstorage.Catalog,
) bool {
	bucket := owner.handle.Bucket
	if p.currentFile(bucket) {
		return false
	}

	pending, err := store.HasPending(ctx)
	if err != nil {
		if !recoveryCancellation(ctx, err) {
			p.log.WarnContext(
				ctx,
				"check recovered file completion failed",
				"bucket",
				bucket.ID,
				"err",
				err,
			)
		}

		p.deferDiscovery(bucket, p.now().Add(30*time.Second))

		return false
	}

	if pending {
		return false
	}

	if err := owner.release(); err != nil {
		p.log.WarnContext(
			ctx,
			"release completed recovery file failed",
			"bucket",
			bucket.ID,
			"err",
			err,
		)
		p.deferDiscovery(bucket, p.now().Add(30*time.Second))

		return true
	}

	session.closeFile(p, ctx, bucket)
	p.finalizeFile(ctx, catalog, bucket)

	return true
}

func (p *deferredProcessor) selectRecoveryFile(
	ctx context.Context,
	pass *recoveryPass,
	snapshot *runtimeSnapshot,
) bool {
	if pass.bucket.Path != "" {
		return true
	}

	if !pass.historical {
		if pass.currentDone {
			p.acknowledgePass(pass)
			return false
		}

		pass.bucket = snapshot.bucket
		pass.currentDone = true

		return true
	}

	if pass.files == nil {
		files, err := snapshot.catalog.Recovery(
			ctx,
			snapshot.cfg.AuditStoragePolicy().Rotation(),
			pass.at,
		)
		if err != nil {
			p.recoveryTraversalError(ctx, pass, err)
			return false
		}

		pass.files = files
	}

	if len(pass.buckets) == 0 {
		buckets, err := pass.files.Next()
		if errors.Is(err, io.EOF) {
			p.acknowledgePass(pass)
			return false
		}

		if err != nil {
			p.recoveryTraversalError(ctx, pass, err)
			return false
		}

		pass.buckets = buckets
		if len(buckets) == 0 {
			return false
		}
	}

	pass.bucket = pass.buckets[0]
	pass.buckets = pass.buckets[1:]

	return true
}

func (p *deferredProcessor) recoveryTraversalError(
	ctx context.Context,
	pass *recoveryPass,
	err error,
) {
	if !recoveryCancellation(ctx, err) {
		p.log.WarnContext(ctx, "discover audit files failed", "err", err)
	}

	// An incomplete traversal requires another historical pass, including when
	// its directory disappears. A removed individual bucket can be skipped.
	p.service.mu.Lock()
	earlierDeadline(&p.service.recovery.historyDeadline, p.now().Add(30*time.Second).Round(0))
	p.service.mu.Unlock()
	p.acknowledgePass(pass)
}

func (p *deferredProcessor) recoveryError(
	ctx context.Context,
	pass *recoveryPass,
	bucket auditstorage.Bucket,
	err error,
) {
	if auditstorage.MatchesOnly(err, os.ErrNotExist) {
		if pass.historical && pass.files != nil {
			p.deferDiscovery(bucket, p.now().Add(30*time.Second))
		}

		pass.clearFile()

		return
	}

	if !recoveryCancellation(ctx, err) {
		p.log.WarnContext(ctx, "recover audit file failed", "bucket", bucket.ID, "err", err)
	}

	p.deferDiscovery(bucket, p.now().Add(30*time.Second))
	pass.clearFile()
}

func (p *deferredProcessor) recoverPage(
	ctx context.Context,
	pass *recoveryPass,
	store *intake.Store,
) (deferredWork, bool, error) {
	var empty deferredWork
	if pass.cursor == nil {
		cursor, err := store.BeginPending(ctx, pass.at)
		if err != nil {
			return empty, false, wrapServerError("capture recovery receipt ceiling", err)
		}

		pass.cursor = &cursor
	}

	entries, err := store.PendingPage(ctx, *pass.cursor)
	if err != nil {
		return empty, false, wrapServerError("read recovery metadata", err)
	}

	for _, entry := range entries {
		if entry.ReceiptID > pass.cursor.MaximumReceiptID {
			pass.cursor.Advance(entry)
			continue
		}

		if p.ownsReceipt(pass.bucket, entry.ReceiptID) {
			pass.cursor.Advance(entry)
			continue
		}

		if entry.ClaimOwner != "" && entry.ClaimExpiresAt != nil &&
			entry.ClaimExpiresAt.After(pass.at) {
			p.deferDiscovery(pass.bucket, *entry.ClaimExpiresAt)
			pass.cursor.Advance(entry)
			continue
		}

		work := deferredWork{
			bucket:    pass.bucket,
			receiptID: entry.ReceiptID,
			epoch:     pass.epoch,
			probe:     false,
		}
		if !p.admitRecovered(work) {
			return empty, false, nil
		}

		pass.cursor.Advance(entry)

		return work, false, nil
	}

	if len(entries) == replayPageLimit {
		return empty, false, nil
	}

	if !pass.cursor.RetryTimes {
		pass.cursor.RetryTimes = true
		pass.cursor.AfterReceiptID = 0
		pass.cursor.AfterTime = ""

		return empty, false, nil
	}

	future, err := store.FirstFuturePending(ctx, pass.at)
	if err != nil {
		return empty, false, wrapServerError("read recovery deadline", err)
	}

	if future != nil && !p.ownsReceipt(pass.bucket, future.ReceiptID) {
		p.deferDiscovery(pass.bucket, *future.NextAttemptAt)
	}

	return empty, true, nil
}

func (p *deferredProcessor) ownsReceipt(bucket auditstorage.Bucket, receiptID int64) bool {
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	key := ReplayKey{BucketID: bucket.ID, ReceiptID: receiptID}
	queued, exists := p.queued[key]
	if exists {
		queued.revisit = true
		p.queued[key] = queued
	}

	return exists
}

func (p *deferredProcessor) admitRecovered(work deferredWork) bool {
	p.service.mu.Lock()
	defer p.service.mu.Unlock()
	p.queueMu.Lock()
	defer p.queueMu.Unlock()
	if p.service.paused || work.epoch != p.service.epoch ||
		slices.Contains(p.service.excluded, work.bucket.Path) {
		return false
	}

	key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
	if _, exists := p.queued[key]; exists {
		return false
	}

	size := replayMetadataBytes + len(work.bucket.ID) + len(work.bucket.Path)
	if p.queuedBytes+size > replayDispatchBytes {
		p.service.recovery.capacityBlocked = true
		p.service.recovery.creditReserved = true

		return false
	}

	p.queued[key] = queuedReplay{size: size, epoch: work.epoch, revisit: false, complete: false}
	p.queuedBytes += size
	p.service.recovery.creditReserved = false

	return true
}

func (p *deferredProcessor) finalizeFile(
	ctx context.Context,
	catalog *auditstorage.Catalog,
	bucket auditstorage.Bucket,
) {
	p.releaseIdleFileOwners(ctx, bucket)
	_, err := catalog.Finalize(ctx, bucket)
	if err != nil {
		if !recoveryCancellation(ctx, err) {
			p.log.WarnContext(
				ctx,
				"finalize retired audit file deferred",
				"bucket",
				bucket.ID,
				"err",
				err,
			)
		}

		p.deferDiscovery(bucket, p.now().Add(30*time.Second))
	}
}

func recoveryCancellation(ctx context.Context, err error) bool {
	cause := ctx.Err()
	return cause != nil && auditstorage.MatchesOnly(err, cause)
}

func (p *deferredProcessor) finishJob(
	ctx context.Context,
	job *deferredProcessor,
	work deferredWork,
	release func(),
	completed bool,
) {
	adapter, ok := job.store.(*sqliteIntakeStore)
	if !ok {
		return
	}

	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	var entry *intake.PendingMetadata
	var err error
	if !completed {
		entry, err = adapter.store.PendingReceipt(cleanup, work.receiptID)
	}

	if err != nil {
		p.log.WarnContext(
			cleanup,
			"read deferred completion state failed",
			"bucket",
			work.bucket.ID,
			"err",
			err,
		)
		p.deferDiscovery(work.bucket, p.now().Add(job.claimLease))

		return
	}

	if entry != nil {
		deadline := p.now().Add(time.Second)
		if entry.NextAttemptAt != nil && entry.NextAttemptAt.After(deadline) {
			deadline = *entry.NextAttemptAt
		}

		if entry.ClaimOwner != "" && entry.ClaimExpiresAt != nil &&
			entry.ClaimExpiresAt.After(deadline) {
			deadline = *entry.ClaimExpiresAt
		}

		p.deferDiscovery(work.bucket, deadline)

		return
	}

	p.queueMu.Lock()
	key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
	queued := p.queued[key]
	queued.complete = true
	p.queued[key] = queued
	p.queueMu.Unlock()
	if !p.currentFile(work.bucket) {
		pending, err := adapter.store.HasPending(cleanup)
		if err != nil {
			p.log.WarnContext(
				cleanup,
				"check retired pending receipts failed",
				"bucket",
				work.bucket.ID,
				"err",
				err,
			)
			p.deferDiscovery(work.bucket, p.now().Add(30*time.Second))

			return
		}

		if pending {
			return
		}

		release()
		p.finalizeFile(cleanup, job.catalog, work.bucket)
	}
}
