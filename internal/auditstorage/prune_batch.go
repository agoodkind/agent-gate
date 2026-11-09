package auditstorage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// PruneTarget preserves file identity between cancellation and deletion.
type PruneTarget struct {
	Path         string
	identityPath string
	identity     os.FileInfo
}

// PruneBatchCandidates selects at most 100 oldest files without retaining history.
func (catalog *Catalog) PruneBatchCandidates(
	ctx context.Context,
	policy RotationPolicy,
	now time.Time,
) ([]PruneTarget, error) {
	lock, err := catalog.coordinate(ctx)
	if err != nil {
		return nil, err
	}

	defer func() { _ = lock.Close() }()
	state, err := catalog.checkStateSummary(ctx, policy)
	if err != nil {
		return nil, err
	}

	inventory, err := catalog.pruningInventory(ctx, policy, now)
	if err != nil {
		return nil, err
	}

	files := catalog.mergeMaintenanceFiles(inventory.oldest, inventory.expired)
	if inventory.blocked != nil {
		files = catalog.mergeMaintenanceFiles(files, []storageFile{*inventory.blocked})
	}

	total := inventory.total
	targets := make([]PruneTarget, 0, maintenanceBatchLimit)
	sizeBlocked := false
	ageBlocked := false

	for _, file := range files {
		bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(file.path))
		expired := !recognized || !retained(bucket, policy, now)
		overSize := policy.MaxBytes > 0 && total > policy.MaxBytes
		if !expired && (!overSize || sizeBlocked || bucket.ID == state.CurrentBucketID) {
			continue
		}

		available, err := catalog.pruneAvailable(file.path)
		if err != nil {
			return nil, err
		}

		if expired && !available {
			if ageBlocked {
				continue
			}

			ageBlocked = true
		}

		target, err := capturePruneTarget(file.path)
		if err != nil {
			return nil, err
		}

		targets = append(targets, target)
		if available {
			total -= file.bytes
		} else if overSize {
			sizeBlocked = true
		}

		if len(targets) == maintenanceBatchLimit {
			return targets, nil
		}
	}

	return targets, nil
}

func (catalog *Catalog) pruneAvailable(path string) (bool, error) {
	lock := catalog.bucketLock(path)
	defer func() { _ = lock.Close() }()
	available, err := lock.TryLock()
	if err != nil {
		return false, storageError("inspect pruning claim", err)
	}

	return available, nil
}

func capturePruneTarget(path string) (PruneTarget, error) {
	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		info, err := os.Lstat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}

		if err != nil {
			return PruneTarget{}, storageError("capture pruning identity", err)
		}

		return PruneTarget{Path: path, identityPath: path + suffix, identity: info}, nil
	}

	return PruneTarget{}, storageError("capture pruning identity", os.ErrNotExist)
}

// PruneBatchSelected rechecks identity and retention after selected jobs cancel.
func (catalog *Catalog) PruneBatchSelected(
	ctx context.Context,
	policy RotationPolicy,
	now time.Time,
	targets []PruneTarget,
) (PruneResult, error) {
	if len(targets) > maintenanceBatchLimit {
		return PruneResult{}, errors.New("pruning batch exceeds 100 files")
	}

	if targets == nil {
		targets = []PruneTarget{}
	}

	return catalog.prune(ctx, policy, now, nil, true, targets)
}

func (catalog *Catalog) pruneBatchFiles(
	ctx context.Context,
	policy RotationPolicy,
	now time.Time,
	targets []PruneTarget,
) (PruneResult, error) {
	state, err := catalog.loadStateSummary(ctx)
	if err != nil {
		return PruneResult{}, err
	}

	inventory, err := catalog.pruningInventory(ctx, policy, now)
	if err != nil {
		return PruneResult{}, err
	}

	var result PruneResult

	selectedFiles := make([]storageFile, 0, len(targets))
	for _, target := range targets {
		bytes, err := bucketBytes(target.Path)
		if err != nil {
			return result, err
		}

		if bytes != 0 {
			selectedFiles = append(selectedFiles, storageFile{path: target.Path, bytes: bytes})
		}
	}

	files := catalog.mergeMaintenanceFiles(inventory.oldest, selectedFiles)
	total := inventory.total
	sizeBlocked := false

	for _, file := range files {
		bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(file.path))
		expired := !recognized || !retained(bucket, policy, now)
		overSize := policy.MaxBytes > 0 && total > policy.MaxBytes
		if !expired && (!overSize || sizeBlocked || bucket.ID == state.CurrentBucketID) {
			continue
		}

		selected, err := samePruneTarget(file.path, targets)
		if err != nil {
			return result, err
		}

		if !selected {
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
		} else {
			result.Deferred = append(result.Deferred, file.path)
			if overSize {
				sizeBlocked = true
			}
		}
	}

	return result, nil
}

func samePruneTarget(path string, targets []PruneTarget) (bool, error) {
	for _, target := range targets {
		if target.Path != path {
			continue
		}

		if target.identityPath != target.Path {
			if _, err := os.Lstat(target.Path); err == nil {
				return false, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, storageError("recheck selected primary", err)
			}
		}

		info, err := os.Lstat(target.identityPath)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}

		if err != nil {
			return false, storageError("recheck pruning identity", err)
		}

		return target.identity != nil && os.SameFile(info, target.identity), nil
	}

	return false, nil
}
