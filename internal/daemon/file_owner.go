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
}

func (owners *fileOwners) borrow(ctx context.Context, catalog *auditstorage.Catalog, bucket auditstorage.Bucket) (*fileOwner, error) {
	owners.mu.Lock()
	defer owners.mu.Unlock()
	if owner := owners.files[bucket.Path]; owner != nil {
		owner.references++
		return owner, nil
	}
	handle, err := catalog.OpenWriter(ctx, bucket)
	if err != nil {
		return nil, wrapServerError("borrow audit file", err)
	}
	owner := &fileOwner{registry: owners, handle: handle, references: 1}
	if owners.files == nil {
		owners.files = make(map[string]*fileOwner)
	}
	owners.files[bucket.Path] = owner
	return owner, nil
}

func (owner *fileOwner) release() error {
	owners := owner.registry
	owners.mu.Lock()
	defer owners.mu.Unlock()
	owner.references--
	if owner.references != 0 {
		return nil
	}
	delete(owners.files, owner.handle.Bucket.Path)
	return wrapServerError("release audit file", owner.handle.Close())
}
