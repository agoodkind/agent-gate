package daemon

import (
	"context"
	"sync"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// fileOwners stores only files with active borrowers.
type fileOwners struct {
	mu    sync.Mutex
	files map[string]*fileOwner
}

type fileOwner struct {
	registry   *fileOwners
	handle     *auditstorage.BucketHandle
	references int
	pending    chan struct{}
}

func (owners *fileOwners) borrow(ctx context.Context, catalog *auditstorage.Catalog, bucket auditstorage.Bucket) (*fileOwner, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, wrapServerError("borrow audit file", err)
		}
		owners.mu.Lock()
		if owner := owners.files[bucket.Path]; owner != nil {
			pending := owner.pending
			if pending == nil {
				owner.references++
				owners.mu.Unlock()
				return owner, nil
			}
			owners.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return nil, wrapServerError("wait for audit file", ctx.Err())
			}
		}
		owner := &fileOwner{registry: owners, handle: nil, references: 1, pending: make(chan struct{})}
		if owners.files == nil {
			owners.files = make(map[string]*fileOwner)
		}
		owners.files[bucket.Path] = owner
		owners.mu.Unlock()
		handle, err := catalog.OpenWriter(ctx, bucket)
		owners.mu.Lock()
		owner.handle = handle
		if err != nil {
			delete(owners.files, bucket.Path)
		}
		close(owner.pending)
		owner.pending = nil
		owners.mu.Unlock()
		if err != nil {
			return nil, wrapServerError("borrow audit file", err)
		}
		return owner, nil
	}
}

func (owner *fileOwner) release() error {
	owners := owner.registry
	owners.mu.Lock()
	owner.references--
	if owner.references != 0 {
		owners.mu.Unlock()
		return nil
	}
	owner.pending = make(chan struct{})
	owners.mu.Unlock()
	err := owner.handle.Close()
	owners.mu.Lock()
	delete(owners.files, owner.handle.Bucket.Path)
	close(owner.pending)
	owner.pending = nil
	owners.mu.Unlock()
	return wrapServerError("release audit file", err)
}

func (owner *fileOwner) retain() *fileOwner {
	owner.registry.mu.Lock()
	owner.references++
	owner.registry.mu.Unlock()
	return owner
}
