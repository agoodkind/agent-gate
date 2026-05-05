package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonListenerCreatesSocketPath(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "agent-gate-listener-test.")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	socketPath := filepath.Join(dir, "daemon.sock")
	lis, err := daemonListener(t.Context(), socketPath)
	if err != nil {
		t.Fatalf("daemonListener: %v", err)
	}
	if err := lis.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(socketPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat socket path: %v", err)
	}
}
