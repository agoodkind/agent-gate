package auditstorage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// BucketHandle pins a database until SQLite closes and its shared lock releases.
type BucketHandle struct {
	Bucket     Bucket
	Database   *sql.DB
	release    func() error
	once       sync.Once
	closeError error
}

// ReadSet pins the retained windows selected by one catalog discovery.
type ReadSet struct{ Handles []*BucketHandle }

// Close closes SQLite before releasing the bucket lock.
func (handle *BucketHandle) Close() error {
	if handle == nil {
		return nil
	}
	handle.once.Do(func() {
		handle.closeError = handle.Database.Close()
		if handle.release != nil {
			handle.closeError = errors.Join(handle.closeError, handle.release())
		}
	})
	return handle.closeError
}

// Close releases every database, including when an earlier close fails.
func (set *ReadSet) Close() error {
	if set == nil {
		return nil
	}
	var result error
	for _, handle := range set.Handles {
		result = errors.Join(result, handle.Close())
	}
	return result
}

func (catalog *Catalog) coordinate(ctx context.Context) (*flock.Flock, error) {
	if err := os.MkdirAll(filepath.Dir(catalog.options.CoordinationPath), 0o700); err != nil {
		return nil, storageError("create coordination directory", err)
	}
	lock := flock.New(catalog.options.CoordinationPath)
	locked, err := lock.TryLockContext(ctx, time.Second)
	if err != nil || !locked {
		_ = lock.Close()
		if err == nil {
			err = ctx.Err()
		}
		return nil, storageError("lock audit catalog", err)
	}
	return lock, nil
}

func (catalog *Catalog) bucketLock(path string) *flock.Flock {
	// Hashing avoids user path syntax and keeps each database identity distinct.
	// Only the coordination lock survives a completed deletion.
	digest := sha256.Sum256([]byte(path))
	return flock.New(fmt.Sprintf("%s.%x", catalog.options.CoordinationPath, digest))
}

func (catalog *Catalog) pin(path string) (func() error, error) {
	lock := catalog.bucketLock(path)
	locked, err := lock.TryRLock()
	if err != nil || !locked {
		_ = lock.Close()
		if err == nil {
			err = errors.New("bucket is being removed")
		}
		return nil, storageError("lock bucket "+path, err)
	}
	return lock.Close, nil
}
