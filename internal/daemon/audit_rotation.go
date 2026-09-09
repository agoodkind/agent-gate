package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func auditRetryDelay(attempt int) time.Duration {
	if attempt > 5 {
		return 30 * time.Second
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Second << (attempt - 1)
}

func waitAuditRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return wrapServerError("wait audit retry", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func retryAuditStorage(ctx context.Context, log *slog.Logger, wait func(context.Context, time.Duration) error, operation func() error) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
		err := operation()
		if err == nil {
			return nil
		}
		log.WarnContext(ctx, "audit storage transition failed; retrying", "attempt", attempt, "err", err)
		if err := wait(ctx, auditRetryDelay(attempt)); err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
	}
}

func (s *Server) initializeAuditStorage(ctx context.Context, cfg *config.Config) (*runtimeSnapshot, error) {
	if cfg.Unusable() {
		var snapshot runtimeSnapshot
		snapshot.cfg = cfg
		return &snapshot, nil
	}
	var snapshot *runtimeSnapshot
	err := retryAuditStorage(ctx, s.log, s.retryWait, func() error {
		if s.catalog == nil {
			catalog, err := auditstorage.NewCatalog(cfg.AuditCatalogOptions())
			if err != nil {
				return wrapServerError("reconcile audit storage", err)
			}
			s.catalog = catalog
		}
		catalog := s.catalog
		policy := cfg.AuditStoragePolicy().Rotation()
		bucket, err := catalog.EnsureCurrent(ctx, policy, s.now())
		if errors.Is(err, auditstorage.ErrPolicyChanged) || errors.Is(err, auditstorage.ErrResetPending) {
			bucket, err = catalog.Reset(ctx, policy, s.now())
		}
		if err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
		snapshot, err = s.snapshotForBucket(ctx, cfg, catalog, bucket)
		if err == nil {
			s.catalog = catalog
		}
		return wrapServerError("reconcile audit storage", err)
	})
	return snapshot, err
}

func (s *Server) snapshotForBucket(ctx context.Context, cfg *config.Config, catalog *auditstorage.Catalog, bucket auditstorage.Bucket) (*runtimeSnapshot, error) {
	handle, err := catalog.OpenWriter(ctx, bucket)
	if err != nil {
		return nil, wrapServerError("prepare audit storage", err)
	}
	snapshot, err := newRuntimeSnapshotForBucket(context.WithoutCancel(ctx), cfg, s.log, s.hotKV, s.inferRuntime, handle)
	if err != nil {
		return nil, wrapServerError("create runtime for audit bucket", errors.Join(err, handle.Close()))
	}
	snapshot.deferredProcessor.catalog = catalog
	snapshot.deferredProcessor.bucket = bucket
	snapshot.deferredProcessor.now = s.now
	snapshot.deferredProcessor.notify = s.wakeAuditScheduler
	return snapshot, nil
}

func (s *Server) reconcileAuditStorage(ctx context.Context, now time.Time) error {
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	if err := ctx.Err(); err != nil {
		return wrapServerError("reconcile audit storage", err)
	}
	current := s.runtime.Load()
	if current == nil || current.cfg.Unusable() || s.closing {
		return nil
	}
	policy := current.cfg.AuditStoragePolicy().Rotation()
	if !now.Before(current.bucket.Start.Add(policy.Interval)) || now.Before(current.bucket.Start) {
		bucket, err := s.catalog.EnsureCurrent(ctx, policy, now)
		if err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
		replacement, err := s.snapshotForBucket(ctx, current.cfg, s.catalog, bucket)
		if err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
		s.runtimeMu.Lock()
		old := s.runtime.Swap(replacement)
		s.runtimeMu.Unlock()
		old.close(ctx, s.log)
	}
	_, err := s.catalog.Prune(ctx, policy, now)
	return wrapServerError("reconcile audit storage", err)
}

func (s *Server) replaceAuditSnapshot(ctx context.Context, candidate *config.Config) (*runtimeSnapshot, error) {
	catalog, err := auditstorage.NewCatalog(candidate.AuditCatalogOptions())
	if err != nil {
		return nil, wrapServerError("prepare audit storage", err)
	}
	old := s.runtime.Load()
	if old != nil && old.cfg.Unusable() {
		s.catalog = catalog
		return s.initializeAuditStorage(ctx, candidate)
	}
	if old != nil && !old.cfg.Unusable() &&
		s.catalog.SameStorage(catalog) &&
		old.cfg.AuditStoragePolicy().Rotation() == candidate.AuditStoragePolicy().Rotation() {
		return s.snapshotForBucket(ctx, candidate, s.catalog, old.bucket)
	}
	// Admission stops before old borrowers close. Failed cuts keep history hidden.
	if old != nil && old.deferredProcessor != nil {
		old.deferredProcessor.Close()
	}
	s.runtimeMu.Lock()
	old = s.runtime.Swap(nil)
	s.runtimeMu.Unlock()
	old.close(ctx, s.log)
	var replacement *runtimeSnapshot
	err = retryAuditStorage(ctx, s.log, s.retryWait, func() error {
		bucket, err := catalog.Reset(ctx, candidate.AuditStoragePolicy().Rotation(), s.now())
		if err != nil {
			return wrapServerError("reconcile audit storage", err)
		}
		replacement, err = s.snapshotForBucket(ctx, candidate, catalog, bucket)
		if err != nil {
			return wrapServerError("open reset audit storage", err)
		}
		s.catalog = catalog
		return nil
	})
	return replacement, err
}
