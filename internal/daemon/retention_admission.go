package daemon

import (
	"context"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func (p *deferredProcessor) cancelExpired(ctx context.Context, policy auditstorage.RotationPolicy, now time.Time) {
	p.service.mu.Lock()
	var release []*fileOwner
	for _, active := range p.service.active {
		if active.bucket.Path != "" && !policy.Retains(active.bucket, now) {
			active.cancel()
		}
		if active.session == nil || active.session.owner == nil {
			continue
		}
		owner := active.session.owner
		if owner.handle.Bucket.Path != active.path && !policy.Retains(owner.handle.Bucket, now) {
			active.session.owner = nil
			release = append(release, owner)
		}
	}
	p.queueMu.Lock()
	remaining := make([]deferredWork, 0, len(p.events))
	for _, work := range drainDeferredWork(p.events) {
		if policy.Retains(work.bucket, now) {
			remaining = append(remaining, work)
			continue
		}
		key := ReplayKey{BucketID: work.bucket.ID, ReceiptID: work.receiptID}
		p.queuedBytes -= p.queued[key].size
		delete(p.queued, key)
		p.service.recovery.capacityBlocked = false
	}
	for _, work := range remaining {
		p.events <- work
	}
	p.queueMu.Unlock()
	p.service.mu.Unlock()
	for _, owner := range release {
		releaseSessionOwner(p, ctx, owner)
	}
	p.wakeWorkers()
}
