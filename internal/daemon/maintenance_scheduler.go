package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
)

const maintenanceLeaseTTL = 5 * time.Minute

type maintenanceRunner func(
	context.Context,
	string,
	config.AuditStoragePolicy,
	time.Time,
) (auditmaintenance.Result, error)

type maintenanceTimer struct {
	channel <-chan time.Time
	stop    func()
}

func newMaintenanceTimer(duration time.Duration) maintenanceTimer {
	timer := time.NewTimer(duration)
	return maintenanceTimer{channel: timer.C, stop: func() { _ = timer.Stop() }}
}

func runAuditMaintenance(
	ctx context.Context,
	path string,
	policy config.AuditStoragePolicy,
	now time.Time,
) (auditmaintenance.Result, error) {
	result, err := auditmaintenance.Apply(ctx, auditmaintenance.ApplyOptions{
		Path: path, Policy: policy, Now: now,
		Owner: "agent-gate-daemon", LeaseTTL: maintenanceLeaseTTL, Log: nil,
	})
	if err != nil {
		slog.ErrorContext(ctx, "apply scheduled audit maintenance failed", "err", err)
		return result, fmt.Errorf("apply scheduled audit maintenance: %w", err)
	}
	return result, nil
}

// StartMaintenanceScheduler starts a full-interval maintenance timer.
func (s *Server) StartMaintenanceScheduler(ctx context.Context) {
	s.maintenanceStartMu.Lock()
	defer s.maintenanceStartMu.Unlock()
	if ctx == nil {
		return
	}
	snapshot := s.runtime.Load()
	if snapshot == nil || snapshot.cfg == nil {
		return
	}
	interval := snapshot.cfg.AuditStoragePolicy().MaintenanceInterval
	if interval <= 0 {
		return
	}
	s.stopMaintenanceSchedulerLocked()
	s.startMaintenanceSchedulerLocked(ctx, snapshot, interval)
}

func (s *Server) stopMaintenanceSchedulerLocked() {
	s.cfgMu.Lock()
	previousCancel := s.maintenanceCancel
	previousDone := s.maintenanceDone
	s.maintenanceCancel = nil
	s.maintenanceDone = nil
	s.cfgMu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	if previousDone != nil {
		<-previousDone
	}
}

func (s *Server) stopMaintenanceSchedulerForReloadLocked() bool {
	s.cfgMu.Lock()
	started := s.maintenanceStarted
	s.cfgMu.Unlock()
	if started {
		s.stopMaintenanceSchedulerLocked()
	}
	return started
}

func (s *Server) restartMaintenanceSchedulerAfterReloadLocked(
	ctx context.Context,
	snapshot *runtimeSnapshot,
	started bool,
) {
	if !started {
		return
	}
	interval := snapshot.cfg.AuditStoragePolicy().MaintenanceInterval
	s.startMaintenanceSchedulerLocked(context.WithoutCancel(ctx), snapshot, interval)
}

func (s *Server) startMaintenanceSchedulerLocked(
	ctx context.Context,
	snapshot *runtimeSnapshot,
	interval time.Duration,
) {
	s.cfgMu.Lock()
	if s.closing {
		s.cfgMu.Unlock()
		return
	}
	schedulerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.maintenanceCancel = cancel
	s.maintenanceDone = done
	s.maintenanceStarted = true
	timerFactory := s.maintenanceTimerFactory
	if timerFactory == nil {
		timerFactory = newMaintenanceTimer
	}
	timer := timerFactory(interval)
	s.cfgMu.Unlock()

	go func() {
		defer close(done)
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.ErrorContext(schedulerCtx, "audit maintenance scheduler panic", "err", recovered)
			}
		}()
		s.runMaintenanceScheduler(schedulerCtx, timer, snapshot.cfg.AuditSQLitePath(), interval)
	}()
}

// Close shuts down daemon-owned resources.
func (s *Server) Close() {
	s.maintenanceStartMu.Lock()
	defer s.maintenanceStartMu.Unlock()
	s.cfgMu.Lock()
	s.closing = true
	updateCancel := s.updateCancel
	maintenanceCancel := s.maintenanceCancel
	maintenanceDone := s.maintenanceDone
	s.updateCancel = nil
	s.maintenanceCancel = nil
	s.maintenanceDone = nil
	s.maintenanceStarted = false
	s.runtimeMu.Lock()
	snapshot := s.runtime.Swap(nil)
	s.runtimeMu.Unlock()
	s.cfgMu.Unlock()

	if s.configWatcher != nil {
		_ = s.configWatcher.Close()
	}
	if updateCancel != nil {
		updateCancel()
	}
	if maintenanceCancel != nil {
		maintenanceCancel()
	}
	if maintenanceDone != nil {
		<-maintenanceDone
	}
	snapshot.close(context.Background(), s.log)
	if s.inferRuntime != nil {
		s.inferRuntime.Close()
	}
	if s.hotKV != nil {
		s.hotKV.Close()
	}
	s.log.InfoContext(context.Background(), "daemon closed")
}

func (s *Server) runMaintenanceScheduler(
	ctx context.Context,
	timer maintenanceTimer,
	path string,
	interval time.Duration,
) {
	nextAttempt := s.maintenanceNow().UTC().Add(interval)
	s.persistMaintenanceNextAttempt(ctx, path, nextAttempt)
	for {
		select {
		case <-ctx.Done():
			timer.stop()
			return
		case firedAt := <-timer.channel:
			timer.stop()
			if ctx.Err() != nil {
				return
			}
			snapshot := s.runtime.Load()
			if snapshot == nil || snapshot.cfg == nil {
				return
			}
			policy := snapshot.cfg.AuditStoragePolicy()
			path = snapshot.cfg.AuditSQLitePath()
			result, err := s.maintenanceRunner(ctx, path, policy, firedAt.UTC())
			logScheduledMaintenanceResult(ctx, s.log, result, err)
			interval = policy.MaintenanceInterval
			if interval <= 0 {
				return
			}
			timer = s.maintenanceTimerFactory(interval)
			nextAttempt = s.maintenanceNow().UTC().Add(interval)
			s.persistMaintenanceNextAttempt(ctx, path, nextAttempt)
		}
	}
}

func logScheduledMaintenanceResult(
	ctx context.Context,
	log *slog.Logger,
	result auditmaintenance.Result,
	runErr error,
) {
	switch {
	case runErr != nil:
		log.ErrorContext(ctx, "audit maintenance scheduled run failed",
			"run_id", result.RunID, "result", result.Result,
			"detail_graphs", result.DetailGraphs, "summary_graphs", result.SummaryGraphs,
			"reclaimed_bytes", result.ReclaimedBytes, "err", runErr)
	case result.Err != nil:
		log.WarnContext(ctx, "audit maintenance scheduled run deferred",
			"run_id", result.RunID, "result", result.Result,
			"detail_graphs", result.DetailGraphs, "summary_graphs", result.SummaryGraphs,
			"reclaimed_bytes", result.ReclaimedBytes, "err", result.Err)
	default:
		log.InfoContext(ctx, "audit maintenance scheduled run completed",
			"run_id", result.RunID, "result", result.Result,
			"detail_graphs", result.DetailGraphs, "summary_graphs", result.SummaryGraphs,
			"reclaimed_bytes", result.ReclaimedBytes)
	}
}

func (s *Server) persistMaintenanceNextAttempt(
	ctx context.Context,
	path string,
	nextAttempt time.Time,
) {
	if err := s.maintenanceWriteNextAttempt(ctx, path, nextAttempt); err != nil &&
		!errors.Is(err, context.Canceled) {
		s.log.ErrorContext(ctx, "write audit maintenance next attempt failed", "err", err)
	}
}
