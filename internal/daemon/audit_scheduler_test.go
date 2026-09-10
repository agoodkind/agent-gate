package daemon

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

func TestAuditSchedulerWaitsForUsableReloadAndStartsOnce(t *testing.T) {
	server, ticks, clockReads := unusableSchedulerServer(t)
	server.StartAuditScheduler(t.Context())
	server.StartAuditScheduler(t.Context())
	if auditSchedulerAllocated(server) || clockReads.Load() != 0 {
		t.Fatal("unusable startup allocated an audit scheduler")
	}
	reloadAuditTestConfig(t, server, config.DefaultAuditSQLitePath(), "24h", 7)
	select {
	case <-server.auditStarted:
	case <-time.After(time.Second):
		t.Fatal("valid reload did not start the requested audit scheduler")
	}
	clockReads.Store(0)
	for range 3 {
		server.StartAuditScheduler(t.Context())
		reloadAuditTestConfig(t, server, config.DefaultAuditSQLitePath(), "24h", 7)
	}
	if got := clockReads.Load(); got != 0 {
		t.Fatalf("later starts/reloads created %d extra scheduler clock reads", got)
	}
	select {
	case ticks <- time.Now():
	case <-time.After(time.Second):
		t.Fatal("audit scheduler stopped after reload returned")
	}
	waitRuntimeCondition(t, func() bool { return clockReads.Load() > 0 })
	if got := clockReads.Load(); got != 1 {
		t.Fatalf("one timer event produced %d clock reads", got)
	}
	server.Close()
	server.StartAuditScheduler(t.Context())
	select {
	case ticks <- time.Now():
		t.Fatal("audit scheduler still consumes timers after Server.Close")
	default:
	}
	if got := clockReads.Load(); got != 1 {
		t.Fatalf("closed scheduler read the clock again: %d", got)
	}
}

func TestAuditReloadPreservesTransportReadinessBoundary(t *testing.T) {
	server, _, clockReads := unusableSchedulerServer(t)
	reloadAuditTestConfig(t, server, config.DefaultAuditSQLitePath(), "24h", 7)
	if auditSchedulerAllocated(server) {
		t.Fatal("reload started audit scheduling before transport readiness")
	}
	clockReads.Store(0)
	server.StartAuditScheduler(t.Context())
	select {
	case <-server.auditStarted:
	case <-time.After(time.Second):
		t.Fatal("readiness did not start the valid audit scheduler")
	}
	if got := clockReads.Load(); got != 1 {
		t.Fatalf("initial scheduler clock reads = %d, want 1", got)
	}
}

func unusableSchedulerServer(t *testing.T) (*Server, chan time.Time, *atomic.Int64) {
	t.Helper()
	setDaemonTestDirs(t)
	path := filepath.Join(t.TempDir(), "invalid.toml")
	writeConfig(t, path, "[audit.storage\n")
	cfg, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatal(err)
	}
	clockReads := &atomic.Int64{}
	now := func() time.Time {
		clockReads.Add(1)
		return time.Now()
	}
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	ticks := make(chan time.Time)
	server.auditTicks = ticks
	return server, ticks, clockReads
}

func auditSchedulerAllocated(server *Server) bool {
	server.cfgMu.RLock()
	defer server.cfgMu.RUnlock()
	return server.auditCancel != nil
}
