package auditstorage

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type storageFile struct {
	path  string
	bytes int64
}

// PruneCandidates selects files before the daemon cancels their queued replay work.
func (catalog *Catalog) PruneCandidates(ctx context.Context, policy RotationPolicy, now time.Time) ([]string, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Close() }()
	state, err := catalog.checkState(policy)
	if err != nil {
		return nil, err
	}
	files, total, err := catalog.storageFiles()
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, file := range files {
		bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(file.path))
		expired := !recognized || !retained(bucket, policy, now)
		overSize := policy.MaxBytes > 0 && total > policy.MaxBytes
		if expired || overSize && bucket.ID != state.CurrentBucketID {
			paths = append(paths, file.path)
			total -= file.bytes
		}
	}
	return paths, nil
}

func (catalog *Catalog) storageFiles(pendingBases ...string) ([]storageFile, int64, error) {
	var paths []string
	for _, base := range append([]string{catalog.options.BasePath}, pendingBases...) {
		members, err := familyPaths(base)
		if err != nil {
			return nil, 0, err
		}
		paths = append(paths, members...)
	}
	slices.Sort(paths)
	var files []storageFile
	var total int64
	for _, path := range slices.Compact(paths) {
		size, err := bucketBytes(path)
		if err != nil {
			return nil, 0, err
		}
		if size == 0 {
			continue
		}
		if size > math.MaxInt64-total {
			return nil, 0, errors.New("audit storage size overflows bytes")
		}
		files = append(files, storageFile{path: path, bytes: size})
		total += size
	}
	slices.SortFunc(files, func(left storageFile, right storageFile) int {
		leftBucket, _ := parseBucket(catalog.options.BasePath, filepath.Base(left.path))
		rightBucket, _ := parseBucket(catalog.options.BasePath, filepath.Base(right.path))
		return strings.Compare(leftBucket.ID, rightBucket.ID)
	})
	return files, total, nil
}

func (catalog *Catalog) pruneFiles(ctx context.Context, policy RotationPolicy, now time.Time, paths []string, selected bool) (PruneResult, error) {
	var result PruneResult
	files, total, err := catalog.storageFiles()
	if err != nil {
		return result, err
	}
	state, err := catalog.loadState()
	if err != nil {
		return result, err
	}
	sizeBlocked := false
	for _, file := range files {
		bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(file.path))
		expired := !recognized || !retained(bucket, policy, now)
		overSize := policy.MaxBytes > 0 && total > policy.MaxBytes
		if !expired && (!overSize || sizeBlocked || bucket.ID == state.CurrentBucketID) {
			continue
		}
		if selected && !slices.Contains(paths, file.path) {
			if overSize {
				sizeBlocked = true
			}
			continue
		}
		removed, err := catalog.removeBucket(ctx, file.path)
		if err != nil {
			return result, err
		}
		if removed {
			result.Removed = append(result.Removed, file.path)
			total -= file.bytes
			continue
		}
		result.Deferred = append(result.Deferred, file.path)
		if overSize {
			sizeBlocked = true
		}
	}
	return result, nil
}
