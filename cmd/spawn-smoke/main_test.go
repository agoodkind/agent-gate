package main

import (
	"path/filepath"
	"strings"
	"testing"

	agconfig "goodkind.io/agent-gate/internal/config"
)

// TestIsolatedXDGRoutesOffProduction proves the smoke isolation routes the
// daemon socket and the audit DB under the throwaway home, so replayed smoke
// payloads never reach the production daemon or its audit database.
func TestIsolatedXDGRoutesOffProduction(t *testing.T) {
	home := t.TempDir()
	for key, value := range isolatedXDG(home) {
		t.Setenv(key, value)
	}

	socket := agconfig.DaemonSocketPath()
	if !strings.HasPrefix(socket, home) {
		t.Fatalf("daemon socket %q is not under isolated home %q", socket, home)
	}

	auditDB := agconfig.DefaultAuditSQLitePath()
	if !strings.HasPrefix(auditDB, filepath.Join(home, "state")) {
		t.Fatalf("audit DB %q is not under isolated state dir %q", auditDB, filepath.Join(home, "state"))
	}
}
