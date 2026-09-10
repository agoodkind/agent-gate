//go:build auditqualification

package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func newQualificationServer(b *testing.B, cfg *config.Config) (*Server, func()) {
	b.Helper()
	directory := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	b.Setenv("XDG_RUNTIME_DIR", filepath.Join(directory, "runtime"))
	server, err := newServer(context.Background(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(server.Close)
	return server, func() { server.StartAuditScheduler(context.Background()); <-server.auditStarted }
}

func qualificationSeedHistory(b *testing.B, server *Server, cfg *config.Config, pending bool) int {
	b.Helper()
	for day := 6; day >= 0; day-- {
		bucket, err := server.catalog.EnsureCurrent(context.Background(), cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-time.Duration(day)*24*time.Hour))
		if err != nil {
			b.Fatal(err)
		}
		handle, err := server.catalog.OpenWriter(context.Background(), bucket)
		if err != nil {
			b.Fatal(err)
		}
		store, err := intake.NewStore(context.Background(), handle.Database, cfg.AuditStoragePolicy(), newDiscardLogger())
		if err != nil {
			b.Fatal(err)
		}
		qualificationSeedRecords(b, store, day, pending)
		if err := handle.Close(); err != nil {
			b.Fatal(err)
		}
	}
	return 7 * qualificationSeedsPerWindow
}

func qualificationAttach(server *Server, recorder *qualificationRecorder) {
	snapshot := server.runtime.Load()
	snapshot.intakeStore = qualificationIntake{intakeStore: snapshot.intakeStore, metrics: recorder}
	snapshot.evaluationRecorder = recorder
	snapshot.deferredProcessor.evaluationRecorder = recorder
}

func qualificationStatus(b *testing.B, server *Server, cfg *config.Config) {
	b.Helper()
	if _, err := server.catalog.Status(cfg.AuditStoragePolicy().Rotation(), time.Now()); err != nil {
		b.Fatal(err)
	}
}

func qualificationFlushHistory(b *testing.B, server *Server, cfg *config.Config) {
	b.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		readers, err := server.catalog.Read(context.Background(), cfg.AuditStoragePolicy().Rotation(), time.Now())
		if err != nil {
			b.Fatal(err)
		}
		pending := 0
		for _, handle := range readers.Handles {
			store, err := intake.NewStore(context.Background(), handle.Database, cfg.AuditStoragePolicy(), newDiscardLogger())
			if err != nil {
				b.Fatal(err)
			}
			records, err := store.ListDeferredPending(context.Background(), 1)
			if err != nil {
				b.Fatal(err)
			}
			pending += len(records)
		}
		if err := readers.Close(); err != nil {
			b.Fatal(err)
		}
		if pending == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	b.Fatal("timed out flushing retained qualification history")
}
