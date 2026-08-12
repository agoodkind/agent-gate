//go:build unix

package auditmaintenance

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const snapshotLockOrphanParent = "AGENT_GATE_SNAPSHOT_LOCK_ORPHAN_PARENT"

func TestSnapshotCheckpointLockDoesNotCreateSharedMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db-shm")
	release, err := holdSnapshotCheckpointLock(t.Context(), path)
	if err != nil {
		t.Fatalf("holdSnapshotCheckpointLock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shared memory stat error = %v, want not exist", err)
	}
}

func TestSnapshotCheckpointLockReportsHelperFailure(t *testing.T) {
	path := createSnapshotSharedMemory(t)
	t.Setenv(snapshotLockTestFail, "1")

	_, err := holdSnapshotCheckpointLock(t.Context(), path)

	if err == nil || !strings.Contains(err.Error(), "snapshot lock helper test failure") {
		t.Fatalf("error = %v, want helper failure", err)
	}
}

func TestSnapshotCheckpointLockCancellationReapsBlockedHelper(t *testing.T) {
	path := createSnapshotSharedMemory(t)
	release, err := holdSnapshotCheckpointLock(t.Context(), path)
	if err != nil {
		t.Fatalf("hold first lock: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = holdSnapshotCheckpointLock(ctx, path)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("blocked lock error = %v, want canceled", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	nextRelease, err := holdSnapshotCheckpointLock(t.Context(), path)
	if err != nil {
		t.Fatalf("hold lock after cancellation: %v", err)
	}
	if err := nextRelease(); err != nil {
		t.Fatalf("release lock after cancellation: %v", err)
	}
}

func TestSnapshotCheckpointLockReleasesAfterParentExit(t *testing.T) {
	if os.Getenv(snapshotLockOrphanParent) == "1" {
		if _, err := holdSnapshotCheckpointLock(
			t.Context(), os.Getenv("AGENT_GATE_SNAPSHOT_LOCK_ORPHAN_PATH"),
		); err != nil {
			t.Fatalf("hold orphan lock: %v", err)
		}
		return
	}
	path := createSnapshotSharedMemory(t)
	command := exec.CommandContext(
		t.Context(), os.Args[0], "-test.run=^TestSnapshotCheckpointLockReleasesAfterParentExit$",
	)
	command.Env = append(
		os.Environ(),
		snapshotLockOrphanParent+"=1",
		"AGENT_GATE_SNAPSHOT_LOCK_ORPHAN_PATH="+path,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run orphan lock parent: %v\n%s", err, output)
	}

	release, err := holdSnapshotCheckpointLock(t.Context(), path)
	if err != nil {
		t.Fatalf("hold lock after parent exit: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("release lock after parent exit: %v", err)
	}
}

func createSnapshotSharedMemory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db-shm")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create shared memory: %v", err)
	}
	if err := file.Truncate(32 * 1024); err != nil {
		_ = file.Close()
		t.Fatalf("size shared memory: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close shared memory: %v", err)
	}
	return path
}
