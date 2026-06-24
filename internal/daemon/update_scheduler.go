package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/clock"
	"goodkind.io/agent-gate/internal/config"
	updater "goodkind.io/agent-gate/internal/update"
	"goodkind.io/agent-gate/internal/version"
)

const (
	updateDisabledPollInterval = time.Minute
	updateInitialDelay         = time.Minute
)

// StartUpdateScheduler runs the daemon-owned update loop.
func (s *Server) StartUpdateScheduler(ctx context.Context, stopDaemon func()) {
	if stopDaemon == nil {
		return
	}
	schedulerCtx, cancel := context.WithCancel(ctx)
	s.cfgMu.Lock()
	if s.updateCancel != nil {
		s.updateCancel()
	}
	s.updateCancel = cancel
	s.stopDaemon = stopDaemon
	s.cfgMu.Unlock()
	go func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.ErrorContext(schedulerCtx, "update scheduler panic", "err", r)
			}
		}()
		s.runUpdateScheduler(schedulerCtx, stopDaemon)
	}()
}

func (s *Server) runUpdateScheduler(ctx context.Context, stopDaemon func()) {
	for {
		snapshot := s.runtime.Load()
		if snapshot == nil {
			return
		}
		cfg := snapshot.cfg
		delay := nextUpdateDelay(cfg)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if err := s.runScheduledUpdate(ctx, cfg); err != nil {
			s.log.WarnContext(ctx, "scheduled update failed", "err", err)
			continue
		}
		state, err := updater.LoadState("")
		if err == nil && state.LastResult == "applied" {
			stopDaemon()
			return
		}
	}
}

func nextUpdateDelay(cfg *config.Config) time.Duration {
	if cfg == nil || !cfg.UpdateEnabled() {
		return updateDisabledPollInterval
	}
	state, err := updater.LoadState("")
	if err == nil && !state.NextCheckAt.IsZero() {
		delay := clock.Until(state.NextCheckAt)
		if delay > 0 {
			return delay
		}
	}
	return jitterDuration(updateInitialDelay)
}

func (s *Server) runScheduledUpdate(ctx context.Context, cfg *config.Config) error {
	if cfg == nil || !cfg.UpdateEnabled() {
		return nil
	}
	options := updater.Options{
		Config:      cfg,
		Client:      nil,
		InstallPath: "",
		CacheDir:    "",
		StatePath:   "",
		DryRun:      false,
		Log:         s.log.With(slog.String("component", "update")),
	}
	switch cfg.UpdateMode() {
	case config.UpdateModeCheck:
		_, err := updater.Check(ctx, options)
		if err != nil {
			s.log.WarnContext(ctx, "scheduled update check failed", "err", err)
			return fmt.Errorf("scheduled update check: %w", err)
		}
		return nil
	case config.UpdateModeApply:
		if version.Version == "dev" || version.Version == "unknown" {
			s.log.DebugContext(ctx, "scheduled update apply skipped for development build", "version", version.Version, "commit", version.Commit)
			_, err := updater.Check(ctx, options)
			if err != nil {
				s.log.WarnContext(ctx, "scheduled update fallback check failed", "err", err)
				return fmt.Errorf("scheduled update fallback check: %w", err)
			}
			return nil
		}
		_, err := updater.Apply(ctx, options)
		if err != nil {
			s.log.WarnContext(ctx, "scheduled update apply failed", "err", err)
			return fmt.Errorf("scheduled update apply: %w", err)
		}
		return nil
	default:
		return nil
	}
}

func jitterDuration(base time.Duration) time.Duration {
	if base <= 0 {
		return base
	}
	maxJitter := int64(base / 10)
	if maxJitter <= 0 {
		return base
	}
	offset := clock.Now().UnixNano() % maxJitter
	return base + time.Duration(offset)
}
