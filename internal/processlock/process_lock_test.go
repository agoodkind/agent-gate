//go:build unix

package processlock_test

import (
	"errors"
	"testing"

	"goodkind.io/agent-gate/internal/processlock"
)

func TestDaemonProcessLockPreservesNonblockingBusyBehavior(t *testing.T) {
	directory := t.TempDir()
	first, err := processlock.Acquire(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Release() }()
	if _, err := processlock.Acquire(directory); !errors.Is(err, processlock.ErrBusy) {
		t.Fatalf("second acquire error = %v, want busy", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}
	second, err := processlock.Acquire(directory)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
