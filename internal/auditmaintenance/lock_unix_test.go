//go:build unix

package auditmaintenance

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMaintenanceFileLockIsNonblocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	first, err := acquireFileLock(path)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	defer func() { _ = first.release() }()
	_, err = acquireFileLock(path)
	if !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("second lock error = %v, want maintenance busy", err)
	}
}
