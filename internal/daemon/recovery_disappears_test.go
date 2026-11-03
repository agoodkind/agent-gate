package daemon_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestRecoveryRetriesWhenDirectoryDisappearsDuringTraversal(t *testing.T) {
	harness := newOwnershipHarness(t, 1, false, false)
	harness.server.Close()
	directory := filepath.Join(t.TempDir(), "sqlite")
	harness.base = filepath.Join(directory, "audit.db")
	harness.writeConfig(t, 1, "probe")
	cfg, err := config.LoadExisting(config.Path())
	if err != nil {
		t.Fatal(err)
	}
	harness.cfg = cfg
	if _, err := ownershipCatalog(t, cfg).Reset(t.Context(), cfg.AuditStoragePolicy().Rotation(), time.Now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	seedRecoveryBacklog(t, harness, 2)
	logPath := filepath.Join(t.TempDir(), "probe.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	server, err := daemon.New(t.Context(), slog.New(slog.NewTextHandler(logFile, nil)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	harness.server = server
	server.StartAuditScheduler(t.Context())
	first := harness.nextCall(t)
	if !strings.Contains(first.input, "backlog-0") {
		t.Fatalf("first historical input = %s", first.input)
	}
	harness.accept(t, "current-before-rename")
	close(first.release)
	current := harness.nextCall(t)
	if !strings.Contains(current.input, "current-before-rename") {
		t.Fatalf("next input = %s", current.input)
	}
	unavailable := directory + "-unavailable"
	if err := os.Rename(directory, unavailable); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			_ = os.Rename(unavailable, directory)
		}
	}()
	close(current.release)
	time.Sleep(2500 * time.Millisecond)
	if err := os.Rename(unavailable, directory); err != nil {
		t.Fatal(err)
	}
	restored = true
	harness.accept(t, "current-after-restore")
	after := harness.nextCall(t)
	if !strings.Contains(after.input, "current-after-restore") {
		t.Fatalf("input after restore = %s", after.input)
	}
	close(after.release)
	select {
	case call := <-harness.service.entered:
		if !strings.Contains(call.input, "backlog-1") {
			t.Fatalf("unexpected recovery inference: %s", call.input)
		}
		close(call.release)
		ownershipWait(t, func() bool { return ownershipOutcomes(t, cfg) == [4]int{4, 4, 4, 4} })
	case <-time.After(40 * time.Second):
		t.Fatalf("historical receipt did not recover after directory restoration; log=%s", readRecoveryLog(t, logPath))
	}
}
