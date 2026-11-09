package daemon

import (
	"context"
	"errors"
	"slices"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// recoverySession retains one historical owner within an admitted worker slot.
// The service mutex protects its reference; each receipt owns a separate borrow.
type recoverySession struct{ owner *fileOwner }

var errRecoveryTransition = errors.New("historical admission waits for storage transition")

func releaseSessionOwner(p *deferredProcessor, ctx context.Context, owner *fileOwner) {
	if owner == nil {
		return
	}

	if err := owner.release(); err != nil {
		p.log.WarnContext(
			ctx,
			"release recovery session failed",
			"bucket",
			owner.handle.Bucket.ID,
			"err",
			err,
		)
	}
}

func (session *recoverySession) close(p *deferredProcessor, ctx context.Context) {
	p.service.mu.Lock()
	owner := session.owner
	session.owner = nil
	p.service.mu.Unlock()
	releaseSessionOwner(p, ctx, owner)
}

func (session *recoverySession) closeFile(
	p *deferredProcessor,
	ctx context.Context,
	bucket auditstorage.Bucket,
) {
	p.service.mu.Lock()
	var owner *fileOwner
	if session.owner != nil && session.owner.handle.Bucket.Path == bucket.Path {
		owner = session.owner
		session.owner = nil
	}

	p.service.mu.Unlock()
	releaseSessionOwner(p, ctx, owner)
}

func (session *recoverySession) prepare(
	p *deferredProcessor,
	ctx context.Context,
	bucket auditstorage.Bucket,
) {
	p.service.mu.Lock()
	var owner *fileOwner
	if session.owner != nil {
		path := session.owner.handle.Bucket.Path
		if slices.Contains(p.service.excluded, path) || !p.service.recovery.pending() ||
			p.service.transition ||
			!p.currentFile(bucket) && path != bucket.Path {
			owner = session.owner
			session.owner = nil
		}
	}

	p.service.mu.Unlock()
	releaseSessionOwner(p, ctx, owner)
}

func (session *recoverySession) borrow(
	ctx context.Context,
	p *deferredProcessor,
	catalog *auditstorage.Catalog,
	bucket auditstorage.Bucket,
) (*fileOwner, error) {
	session.prepare(p, ctx, bucket)
	p.service.mu.Lock()
	if p.service.transition && !p.currentFile(bucket) {
		p.service.mu.Unlock()
		return nil, errRecoveryTransition
	}

	if session.owner != nil && session.owner.handle.Bucket.Path == bucket.Path {
		owner := session.owner.retain()
		p.service.mu.Unlock()

		return owner, nil
	}

	version := p.service.cacheVersion
	retain := !p.currentFile(bucket) && !p.service.transition
	p.service.mu.Unlock()
	owner, err := p.service.server.owners.borrow(ctx, catalog, bucket)
	if err != nil {
		return nil, err
	}

	p.service.mu.Lock()
	if retain && !p.service.transition && version == p.service.cacheVersion {
		session.owner = owner.retain()
	}

	p.service.mu.Unlock()

	return owner, nil
}

func (p *deferredProcessor) beginRecoveryTransition(ctx context.Context) {
	p.service.mu.Lock()
	p.service.transition = true
	p.service.cacheVersion++
	var release []*fileOwner

	for _, active := range p.service.active {
		if active.session == nil || active.session.owner == nil {
			continue
		}

		owner := active.session.owner
		if owner.handle.Bucket.Path != active.path {
			active.session.owner = nil
			release = append(release, owner)
		}
	}

	p.service.mu.Unlock()

	for _, owner := range release {
		releaseSessionOwner(p, ctx, owner)
	}
}

func (p *deferredProcessor) endRecoveryTransition() {
	p.service.mu.Lock()
	p.service.transition = false
	p.service.mu.Unlock()
	p.wakeWorkers()
}

func (p *deferredProcessor) releaseIdleFileOwners(ctx context.Context, bucket auditstorage.Bucket) {
	p.service.mu.Lock()
	var release []*fileOwner

	for _, active := range p.service.active {
		if active.session == nil || active.session.owner == nil || active.path == bucket.Path {
			continue
		}

		owner := active.session.owner
		if owner.handle.Bucket.Path == bucket.Path {
			active.session.owner = nil
			release = append(release, owner)
		}
	}

	p.service.mu.Unlock()

	for _, owner := range release {
		releaseSessionOwner(p, ctx, owner)
	}
}
