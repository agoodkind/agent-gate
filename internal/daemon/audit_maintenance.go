package daemon

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

type auditMaintenance struct {
	initialized   bool
	policy        auditstorage.RotationPolicy
	current       string
	directoryTime time.Time
	sizes         map[string]int64
	total         int64
	retryAt       time.Time
}

func (s *Server) maintenanceSamples(snapshot *runtimeSnapshot) (map[string]int64, time.Time, error) {
	s.owners.mu.Lock()
	paths := make([]string, 0, len(s.owners.files))
	for path := range s.owners.files {
		paths = append(paths, path)
	}
	s.owners.mu.Unlock()
	sizes := make(map[string]int64, len(paths))
	for _, path := range paths {
		size, err := auditstorage.FileBytes(path)
		if err != nil {
			return nil, time.Time{}, wrapServerError("measure active audit file", err)
		}
		sizes[path] = size
	}
	directory, err := os.Stat(filepath.Dir(snapshot.bucket.Path))
	if err != nil {
		return nil, time.Time{}, wrapServerError("stat audit directory", err)
	}
	return sizes, directory.ModTime(), nil
}

func (maintenance *auditMaintenance) needed(s *Server, now time.Time) (bool, error) {
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		return false, nil
	}
	policy := snapshot.cfg.AuditStoragePolicy().Rotation()
	sizes, directoryTime, err := s.maintenanceSamples(snapshot)
	if err != nil {
		return false, err
	}
	rotate, err := snapshot.catalog.NeedsRotation(snapshot.bucket, policy, now)
	if err != nil {
		return false, wrapServerError("inspect audit rotation", err)
	}
	changed := !maintenance.initialized || maintenance.policy != policy || maintenance.current != snapshot.bucket.Path || !directoryTime.Equal(maintenance.directoryTime) || len(sizes) != len(maintenance.sizes)
	for path, size := range sizes {
		previous, exists := maintenance.sizes[path]
		if !exists {
			changed = true
			continue
		}
		maintenance.total += size - previous
	}
	maintenance.sizes = sizes
	if rotate || changed {
		return true, nil
	}
	if !maintenance.retryAt.IsZero() {
		return !now.Before(maintenance.retryAt), nil
	}
	return policy.MaxBytes > 0 && maintenance.total > policy.MaxBytes, nil
}

func (maintenance *auditMaintenance) reconciled(ctx context.Context, s *Server, now time.Time) error {
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg.Unusable() {
		return nil
	}
	sizes, directoryTime, err := s.maintenanceSamples(snapshot)
	if err != nil {
		return err
	}
	total, blocked, err := snapshot.catalog.MaintenanceSize(ctx, snapshot.cfg.AuditStoragePolicy().Rotation(), now)
	if err != nil {
		return wrapServerError("measure audit family", err)
	}
	maintenance.initialized = true
	maintenance.policy = snapshot.cfg.AuditStoragePolicy().Rotation()
	maintenance.current = snapshot.bucket.Path
	maintenance.directoryTime = directoryTime
	maintenance.sizes = sizes
	maintenance.total = total
	maintenance.retryAt = time.Time{}
	if blocked {
		maintenance.retryAt = now.Add(time.Second)
	}
	return nil
}

func (maintenance *auditMaintenance) run(ctx context.Context, s *Server, now time.Time) error {
	needed, err := maintenance.needed(s, now)
	if err != nil || !needed {
		return err
	}
	if err := s.reconcileAuditStorage(ctx, now); err != nil {
		return err
	}
	return maintenance.reconciled(ctx, s, now)
}
