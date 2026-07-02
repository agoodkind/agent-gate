package daemon

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/updateopts"
	"goodkind.io/go-makefile/selfupdate"
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
	selfupdate.RunScheduler(ctx, selfupdate.SchedulerHooks{
		Enabled: func() bool {
			cfg := s.updateConfig()
			return cfg != nil && cfg.UpdateEnabled()
		},
		Mode: func() string {
			cfg := s.updateConfig()
			if cfg == nil {
				return ""
			}
			return cfg.UpdateMode()
		},
		Options: func() selfupdate.Options {
			return updateopts.Options(s.updateConfig(), updateopts.Overrides{
				Client:      nil,
				InstallPath: "",
				DryRun:      false,
				Log:         nil,
			})
		},
		StopForRelaunch: stopDaemon,
		Log:             s.log,
	})
}

func (s *Server) updateConfig() *config.Config {
	snapshot := s.runtime.Load()
	if snapshot == nil {
		return nil
	}
	return snapshot.cfg
}
