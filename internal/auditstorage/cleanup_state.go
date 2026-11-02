package auditstorage

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"
)

func (catalog *Catalog) blockedForCleanup(path string, policy RotationPolicy, now time.Time, selected []string) (bool, error) {
	primary, valid := familyPrimary(catalog.options.BasePath, filepath.Base(path))
	if !valid || primary != path {
		return false, nil
	}
	bucket, recognized := parseBucket(catalog.options.BasePath, filepath.Base(path))
	if recognized && retained(bucket, policy, now) && !slices.Contains(selected, path) {
		return false, nil
	}
	size, err := bucketBytes(path)
	if err != nil || size == 0 {
		return false, err
	}
	if slices.Contains(selected, path) {
		return true, nil
	}
	available, err := catalog.pruneAvailable(path)
	return !available, err
}

func (catalog *Catalog) visitBlockedCleanup(ctx context.Context, policy RotationPolicy, now time.Time, selected []string, visit func(string) error) error {
	return visitFamily(ctx, catalog.options.BasePath, func(path string) error {
		blocked, err := catalog.blockedForCleanup(path, policy, now, selected)
		if err != nil || !blocked {
			return err
		}
		return visit(path)
	})
}

func (catalog *Catalog) saveBatchCleanup(ctx context.Context, policy RotationPolicy, now time.Time, message string, selected []string) error {
	state, savedCount, err := catalog.readStateSummary(ctx)
	if err != nil {
		return err
	}
	pathsChanged := false
	// Catalog coordination prevents new claims while existing readers can
	// release. Validate saved members after counting the current set.
	newCount := 0
	if err := catalog.visitBlockedCleanup(ctx, policy, now, selected, func(string) error { newCount++; return nil }); err != nil {
		return err
	}
	if newCount != savedCount {
		slog.DebugContext(ctx, "counted blocked audit files", "files", newCount, "previous_files", savedCount)
	}
	oldCount := 0
	err = catalog.visitStatePaths(ctx, fieldDeferred, func(path string) error {
		oldCount++
		blocked, err := catalog.blockedForCleanup(path, policy, now, selected)
		if !blocked {
			pathsChanged = true
		}
		return err
	})
	if err != nil {
		return err
	}
	if !pathsChanged && oldCount == newCount {
		if state.CleanupError == message {
			return nil
		}
		state.CleanupError = message
		return catalog.saveStatePreserving(ctx, state, fieldDeferred)
	}
	state.CleanupError = message
	state.DeferredFiles = nil
	return catalog.publishState(func(file *os.File) error {
		data, err := json.Marshal(state)
		if err != nil {
			return storageError("encode cleanup state", err)
		}
		if _, err := file.Write(data[:len(data)-1]); err != nil {
			return storageError("write cleanup state", err)
		}
		return writeStatePathArray(file, fieldDeferred, func(writePath func(string) error) error {
			if newCount == 0 {
				return nil
			}
			return catalog.sortBlockedCleanup(ctx, policy, now, selected, writePath)
		})
	})
}
