package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
)

func newQualificationServer(b *testing.B, cfg *config.Config) (*Server, func()) {
	b.Helper()
	directory := b.TempDir()
	b.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
	b.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	b.Setenv("XDG_RUNTIME_DIR", filepath.Join(directory, "runtime"))
	ready, finished := make(chan struct{}), make(chan struct{})
	original := replayRuntimeSnapshotPending
	replayRuntimeSnapshotPending = func(processor *deferredProcessor, ctx context.Context) error {
		<-ready
		defer close(finished)
		return original(processor, ctx)
	}
	server, err := New(newDiscardLogger(), cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(server.Close)
	b.Cleanup(func() { replayRuntimeSnapshotPending = original })
	return server, func() { close(ready); <-finished }
}

func qualificationSeedHistory(b *testing.B, server *Server, _ *config.Config, pending bool) int {
	b.Helper()
	store := server.runtime.Load().intakeStore.(*sqliteIntakeStore).store
	for day := 6; day >= 0; day-- {
		qualificationSeedRecords(b, store, day, pending)
	}
	return 7 * qualificationSeedsPerWindow
}

func qualificationAttach(server *Server, recorder *qualificationRecorder) {
	snapshot := server.runtime.Load()
	snapshot.intakeStore = qualificationIntake{intakeStore: snapshot.intakeStore, metrics: recorder}
	snapshot.evaluationRecorder = recorder
	snapshot.deferredProcessor.evaluationRecorder = recorder
}

func qualificationStatus(b *testing.B, _ *Server, cfg *config.Config) {
	b.Helper()
	if _, err := auditmaintenance.ReadStatus(context.Background(), cfg.AuditSQLitePath(), cfg.AuditStoragePolicy(), time.Now()); err != nil {
		b.Fatal(err)
	}
}

func qualificationFlushHistory(b *testing.B, server *Server, _ *config.Config) {
	flushAuditTrace(b, server)
}
