package auditmaintenance_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/auditstorage"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/processlock"
)

const fullCompactCrashChild = "AGENT_GATE_FULL_COMPACT_CRASH_CHILD"

func TestFullCompactApplyReclaimsSpaceAndKeepsServiceStopped(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := auditmaintenance.ApplyFullCompact(t.Context(), fullCompactApplyOptions(path))
	if err != nil {
		t.Fatalf("ApplyFullCompact: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("database size = %d, want less than %d", after.Size(), before.Size())
	}
	if result.ReclaimedBytes <= 0 {
		t.Fatalf("reclaimed bytes = %d, want positive", result.ReclaimedBytes)
	}
	assertFullCompactDatabase(t, path)
	assertNoFullCompactRecoveryFiles(t, path)
}

func TestFullCompactApplyEnablesIncrementalAutoVacuum(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), fullCompactApplyOptions(path)); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var mode int
	if err := database.QueryRow(`pragma auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != 2 {
		t.Fatalf("auto-vacuum mode = %d, want 2", mode)
	}
}

func TestFullCompactApplyLeavesSourceModeUnchangedBeforeReplacement(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	beforeMode := readFullCompactMode(t, path)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("copy-verified")

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want injected interruption")
	}

	assertFullCompactMode(t, path, beforeMode)
}

func TestFullCompactApplyVacuumFailureNeverChangesFullSourceMode(t *testing.T) {
	path := createFullCompactApplyDatabaseWithMode(t, 1)
	assertFullCompactMode(t, path, 1)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("before-vacuum")

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want injected vacuum failure")
	}

	assertFullCompactMode(t, path, 1)
}

func TestFullCompactApplyPreservesSourcePermissionsAndOwnership(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), fullCompactApplyOptions(path)); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("replacement permissions = %o, want %o", after.Mode().Perm(), before.Mode().Perm())
	}
	if !sameFullCompactOwner(before, after) {
		t.Fatal("replacement ownership differs from source")
	}
}

func TestFullCompactApplyRejectsRunningServiceBeforeMutation(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	before := snapshotFullCompactFiles(t, path)
	options := fullCompactApplyOptions(path)
	options.InspectService = func(context.Context) (installer.ServiceState, error) {
		return installer.ServiceState{Managed: true, Running: true}, nil
	}
	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "daemon is running") {
		t.Fatalf("ApplyFullCompact error = %v, want running daemon", err)
	}
	after := snapshotFullCompactFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("active-daemon rejection changed files: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactApplyRejectsCanonicalSwapDuringStoppedServiceReinspection(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	swappedBytes, swappedRows := createFullCompactSwapDatabase(t, path)
	options := fullCompactApplyOptions(path)
	inspectionCount := 0
	options.InspectService = func(context.Context) (installer.ServiceState, error) {
		inspectionCount++
		if inspectionCount == 2 {
			if err := os.Rename(path+".swap", path); err != nil {
				t.Fatal(err)
			}
		}
		return installer.ServiceState{
			Platform: "launchd", Managed: true, Running: false, BinaryPath: "/opt/agent-gate",
		}, nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "path changed") {
		t.Fatalf("ApplyFullCompact error = %v, want canonical identity rejection", err)
	}
	assertFullCompactSwapPreserved(t, path, swappedBytes, swappedRows)
	if _, exists, readErr := auditstorage.ReadCutoverJournal(path); readErr != nil || exists {
		t.Fatalf("journal exists/error = %t/%v, want no journal", exists, readErr)
	}
}

func TestFullCompactApplyRejectsCanonicalSwapAfterCopyBeforeJournal(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	swappedBytes, swappedRows := createFullCompactSwapDatabase(t, path)
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "copy-verified" {
			if err := os.Rename(path+".swap", path); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "path changed") {
		t.Fatalf("ApplyFullCompact error = %v, want canonical identity rejection", err)
	}
	assertFullCompactSwapPreserved(t, path, swappedBytes, swappedRows)
	if _, exists, readErr := auditstorage.ReadCutoverJournal(path); readErr != nil || exists {
		t.Fatalf("journal exists/error = %t/%v, want no journal", exists, readErr)
	}
}

func TestFullCompactApplyReleasesLeaseFromDisplacedSourceOnCanonicalSwap(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	swappedBytes, swappedRows := createFullCompactSwapDatabase(t, path)
	displacedPath := path + ".displaced"
	options := fullCompactApplyOptions(path)
	inspectionCount := 0
	options.InspectService = func(context.Context) (installer.ServiceState, error) {
		inspectionCount++
		if inspectionCount == 2 {
			if err := os.Rename(path, displacedPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".swap", path); err != nil {
				t.Fatal(err)
			}
		}
		return installer.ServiceState{
			Platform: "launchd", Managed: true, Running: false, BinaryPath: "/opt/agent-gate",
		}, nil
	}

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want canonical identity rejection")
	}
	database, err := sql.Open("sqlite3", displacedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var leases int
	if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("displaced source leases = %d, want 0", leases)
	}
	assertFullCompactSwapPreserved(t, path, swappedBytes, swappedRows)
	swappedDatabase, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = swappedDatabase.Close() }()
	if err := swappedDatabase.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("swapped canonical leases = %d, want 0", leases)
	}
}

func TestFullCompactApplyRejectsCanonicalSwapAfterFinalCheckBeforeRename(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	swappedBytes, swappedRows := createFullCompactSwapDatabase(t, path)
	displacedPath := path + ".displaced"
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "before-journal:prepared" {
			if err := os.Rename(path, displacedPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".swap", path); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("ApplyFullCompact error = %v, want renamed source identity rejection", err)
	}
	journal, exists, readErr := auditstorage.ReadCutoverJournal(path)
	if readErr != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want recovery journal", exists, readErr)
	}
	assertFullCompactSwapPreserved(t, journal.RollbackPath, swappedBytes, swappedRows)
	assertFullCompactDatabase(t, displacedPath)
	assertFullCompactDatabase(t, journal.WorkingPath)
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canonical database stat error = %v, want missing until explicit recovery", statErr)
	}
	lock, lockErr := processlock.Acquire(options.RuntimeDirectory)
	if lockErr != nil {
		t.Fatalf("reacquire process lock: %v", lockErr)
	}
	if releaseErr := lock.Release(); releaseErr != nil {
		t.Fatalf("release reacquired process lock: %v", releaseErr)
	}
}

func TestFullCompactApplyRejectsMissingRecordedSidecarBeforeReplacement(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	sidecarPath := path + "-wal"
	displacedSidecarPath := path + ".displaced-wal"
	sidecarBytes := []byte("recorded wal sidecar")
	var sourceBytes []byte
	createdSidecar := false
	displacedSidecar := false
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "copy-verified" && !createdSidecar {
			var err error
			sourceBytes, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sidecarPath, sidecarBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			createdSidecar = true
		}
		if step == "before-rename:sidecar-wal" && !displacedSidecar {
			if err := os.Rename(sidecarPath, displacedSidecarPath); err != nil {
				t.Fatal(err)
			}
			displacedSidecar = true
		}
		return nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("ApplyFullCompact error = %v, want sidecar identity rejection", err)
	}
	if !createdSidecar || !displacedSidecar {
		t.Fatalf("sidecar hooks observed = %t/%t, want both", createdSidecar, displacedSidecar)
	}
	canonicalBytes, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(canonicalBytes, sourceBytes) {
		t.Fatal("full compaction committed the stale replacement")
	}
	preservedSidecar, readErr := os.ReadFile(displacedSidecarPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(preservedSidecar, sidecarBytes) {
		t.Fatal("displaced sidecar bytes changed")
	}
	journal, exists, readErr := auditstorage.ReadCutoverJournal(path)
	if readErr != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want recovery journal", exists, readErr)
	}
	if journal.WALIdentity == nil {
		t.Fatal("journal WAL identity = nil, want recorded sidecar")
	}
	if _, statErr := os.Stat(journal.WorkingPath); statErr != nil {
		t.Fatalf("working copy was not preserved: %v", statErr)
	}
	database, openErr := sql.Open("sqlite3", path)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer func() { _ = database.Close() }()
	var leases int
	if queryErr := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); queryErr != nil {
		t.Fatal(queryErr)
	}
	if leases != 0 {
		t.Fatalf("leases = %d, want 0", leases)
	}
	lock, lockErr := processlock.Acquire(options.RuntimeDirectory)
	if lockErr != nil {
		t.Fatalf("reacquire process lock: %v", lockErr)
	}
	if releaseErr := lock.Release(); releaseErr != nil {
		t.Fatalf("release reacquired process lock: %v", releaseErr)
	}
}

func TestFullCompactRecoveryRejectsMissingRecordedSidecar(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	sidecarBytes := []byte("recorded recovery wal sidecar")
	displacedSidecarPath := path + ".displaced-recovery-wal"
	createdSidecar := false
	startedRecovery := false
	displacedSidecar := false
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "copy-verified" && !createdSidecar {
			if err := os.WriteFile(path+"-wal", sidecarBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			createdSidecar = true
		}
		if step == "after-journal:replacement-installed" && !startedRecovery {
			startedRecovery = true
			return errors.New("start rollback")
		}
		if step == "before-rename:recovery-sidecar-wal" && !displacedSidecar {
			journal, exists, err := auditstorage.ReadCutoverJournal(path)
			if err != nil || !exists {
				t.Fatalf("journal exists/error = %t/%v, want recovery journal", exists, err)
			}
			if err := os.Rename(journal.RollbackPath+"-wal", displacedSidecarPath); err != nil {
				t.Fatal(err)
			}
			displacedSidecar = true
		}
		return nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "sidecar") {
		t.Fatalf("ApplyFullCompact error = %v, want recovery sidecar identity rejection", err)
	}
	if !createdSidecar || !startedRecovery || !displacedSidecar {
		t.Fatalf(
			"recovery hooks observed = %t/%t/%t, want all",
			createdSidecar,
			startedRecovery,
			displacedSidecar,
		)
	}
	preservedSidecar, readErr := os.ReadFile(displacedSidecarPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(preservedSidecar, sidecarBytes) {
		t.Fatal("displaced recovery sidecar bytes changed")
	}
	journal, exists, readErr := auditstorage.ReadCutoverJournal(path)
	if readErr != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want recoverable journal", exists, readErr)
	}
	if _, statErr := os.Stat(journal.FailedPath); statErr != nil {
		t.Fatalf("failed replacement was not preserved: %v", statErr)
	}
	assertFullCompactDatabase(t, path)
	options.FailStep = nil
	if _, retryErr := auditmaintenance.ApplyFullCompact(t.Context(), options); retryErr == nil ||
		!strings.Contains(retryErr.Error(), "sidecar") {
		t.Fatalf("retry error = %v, want recovery sidecar identity rejection", retryErr)
	}
	if _, exists, readErr = auditstorage.ReadCutoverJournal(path); readErr != nil || !exists {
		t.Fatalf("journal exists/error after retry = %t/%v, want recoverable journal", exists, readErr)
	}
	preservedSidecar, readErr = os.ReadFile(displacedSidecarPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(preservedSidecar, sidecarBytes) {
		t.Fatal("retry changed displaced recovery sidecar bytes")
	}
	lock, lockErr := processlock.Acquire(options.RuntimeDirectory)
	if lockErr != nil {
		t.Fatalf("reacquire process lock: %v", lockErr)
	}
	if releaseErr := lock.Release(); releaseErr != nil {
		t.Fatalf("release reacquired process lock: %v", releaseErr)
	}
}

func TestFullCompactApplyRejectsProcessLockContentionBeforeCheckpointOrJournal(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	if err := os.MkdirAll(options.RuntimeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	lock, err := processlock.Acquire(options.RuntimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()

	_, err = auditmaintenance.ApplyFullCompact(t.Context(), options)

	if !errors.Is(err, processlock.ErrBusy) {
		t.Fatalf("ApplyFullCompact error = %v, want process lock busy", err)
	}
	if _, exists, readErr := auditstorage.ReadCutoverJournal(path); readErr != nil || exists {
		t.Fatalf("journal exists/error = %t/%v, want no journal", exists, readErr)
	}
	assertFullCompactDatabase(t, path)
}

func TestFullCompactApplyRejectsLeaseContention(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`
		insert into audit_maintenance_lease(singleton, owner, run_id, expires_at)
		values (1, 'other', 'other-run', '2999-01-01T00:00:00Z')
	`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = auditmaintenance.ApplyFullCompact(t.Context(), fullCompactApplyOptions(path))

	if !errors.Is(err, auditmaintenance.ErrMaintenanceBusy) {
		t.Fatalf("ApplyFullCompact error = %v, want maintenance busy", err)
	}
	if _, exists, readErr := auditstorage.ReadCutoverJournal(path); readErr != nil || exists {
		t.Fatalf("journal exists/error = %t/%v, want no journal", exists, readErr)
	}
}

func TestFullCompactApplyRejectsMaintenanceLockContention(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	held := make(chan struct{})
	release := make(chan struct{})
	options.FailStep = func(step string) error {
		if step == "copy-verified" {
			close(held)
			<-release
		}
		return nil
	}
	firstResult := make(chan error, 1)
	go func() {
		_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)
		firstResult <- err
	}()
	<-held

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), fullCompactApplyOptions(path))

	if !errors.Is(err, auditmaintenance.ErrMaintenanceBusy) {
		t.Fatalf("second ApplyFullCompact error = %v, want maintenance busy", err)
	}
	close(release)
	if err := <-firstResult; err != nil {
		t.Fatalf("first ApplyFullCompact: %v", err)
	}
}

func TestFullCompactApplyRestoresDatabaseAfterCopyFailure(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "after-journal:replacement-installed" {
			return errors.New("injected replacement failure")
		}
		return nil
	}
	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)
	if err == nil || !strings.Contains(err.Error(), "injected replacement failure") {
		t.Fatalf("ApplyFullCompact error = %v, want injected failure", err)
	}
	assertFullCompactDatabase(t, path)
	assertFullCompactFailedReplacementPreserved(t, path)
}

func TestFullCompactRecoveryJournalsRestoreAndJournalRemovalBoundaries(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	observed := make(map[string]bool)
	interrupted := false
	options.FailStep = func(step string) error {
		observed[step] = true
		if step == "after-journal:replacement-installed" && !interrupted {
			interrupted = true
			return errors.New("injected replacement failure")
		}
		return nil
	}

	_, err := auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "injected replacement failure") {
		t.Fatalf("ApplyFullCompact error = %v, want injected failure", err)
	}
	for _, step := range []string{
		"before-journal:restoring-replacement",
		"after-journal:restoring-replacement",
		"before-rename:recovery-replacement",
		"after-rename:recovery-replacement",
		"before-journal:replacement-preserved",
		"after-journal:replacement-preserved",
		"before-journal:restoring-original",
		"after-journal:restoring-original",
		"before-rename:recovery-original",
		"after-rename:recovery-original",
		"before-journal:original-restored",
		"after-journal:original-restored",
		"before-journal:restored",
		"after-journal:restored",
		"before-remove:journal",
		"after-remove:journal",
	} {
		if !observed[step] {
			t.Errorf("recovery did not expose %q interruption boundary", step)
		}
	}
	assertFullCompactDatabase(t, path)
}

func TestFullCompactApplyReleasesLeaseAfterCancellation(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	ctx, cancel := context.WithCancel(t.Context())
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "copy-verified" {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	_, err := auditmaintenance.ApplyFullCompact(ctx, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyFullCompact error = %v, want cancellation", err)
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var leases int
	if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("leases = %d, want 0", leases)
	}
}

func TestFullCompactHoldsLeaseAndDaemonProcessLockThroughCopy(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step != "before-vacuum" {
			return nil
		}
		if _, err := processlock.Acquire(options.RuntimeDirectory); !errors.Is(err, processlock.ErrBusy) {
			t.Fatalf("process lock error = %v, want busy", err)
		}
		database, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = database.Close() }()
		var leases int
		if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
			t.Fatal(err)
		}
		if leases != 1 {
			t.Fatalf("leases = %d, want 1", leases)
		}
		return errors.New("stop after lock observation")
	}
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want observation stop")
	}
}

func TestFullCompactHoldsDaemonProcessLockThroughReplacement(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step != "copy-verified" {
			return nil
		}
		if _, err := processlock.Acquire(options.RuntimeDirectory); !errors.Is(err, processlock.ErrBusy) {
			t.Fatalf("process lock error = %v, want busy", err)
		}
		return errors.New("stop after lock observation")
	}
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want observation stop")
	}
}

func TestFullCompactHoldsDaemonProcessLockThroughJournalCleanup(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	observed := false
	options.FailStep = func(step string) error {
		if step != "after-remove:cleanup-failed" {
			return nil
		}
		observed = true
		if _, err := processlock.Acquire(options.RuntimeDirectory); !errors.Is(err, processlock.ErrBusy) {
			t.Fatalf("process lock error = %v, want busy", err)
		}
		return nil
	}
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if !observed {
		t.Fatal("cleanup process lock observation did not run")
	}
}

func TestFullCompactCommittedCleanupFailureLeavesNoRollbackLease(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	journal, exists, err := auditstorage.ReadCutoverJournal(path)
	if err != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want committed journal", exists, err)
	}
	database, err := sql.Open("sqlite3", journal.RollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var leases int
	if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if leases != 0 {
		t.Fatalf("rollback leases = %d, want 0", leases)
	}
}

func TestFullCompactCommittedCleanupRecordsCompletionAndJournalRemoval(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	observed := make(map[string]bool)
	options.FailStep = func(step string) error {
		observed[step] = true
		return nil
	}

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatal(err)
	}

	for _, phase := range []string{
		"cleanup-rollback-complete",
		"cleanup-wal-complete",
		"cleanup-shm-complete",
		"cleanup-working-complete",
		"cleanup-failed-complete",
	} {
		for _, boundary := range []string{"before-journal:", "after-journal:"} {
			step := boundary + phase
			if !observed[step] {
				t.Errorf("cleanup did not expose %q interruption boundary", step)
			}
		}
	}
	for _, step := range []string{"before-remove:journal", "after-remove:journal"} {
		if !observed[step] {
			t.Errorf("cleanup did not expose %q interruption boundary", step)
		}
	}
}

func TestFullCompactRecoveryReacquiresDaemonProcessLockBeforeCleanup(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	lock, err := processlock.Acquire(options.RuntimeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	options.FailStep = nil
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); !errors.Is(err, processlock.ErrBusy) {
		t.Fatalf("recovery error = %v, want process lock busy", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatalf("recovery after process lock release: %v", err)
	}
	assertNoFullCompactRecoveryFiles(t, path)
}

func TestFullCompactCommitJournalWriteFailureRecoversFromDurablePhase(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	temporaryJournalPath := auditstorage.CutoverJournalPath(path) + ".tmp"
	blockerPath := filepath.Join(temporaryJournalPath, "blocker")
	options := fullCompactApplyOptions(path)
	options.FailStep = func(step string) error {
		if step == "before-journal:committed" {
			if err := os.Mkdir(temporaryJournalPath, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blockerPath, []byte("block"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if step == "before-journal:restoring-replacement" ||
			step == "before-journal:cleanup-rollback" {
			if err := os.Remove(blockerPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(temporaryJournalPath); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want journal write failure")
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("commit journal write failure did not restore the original database")
	}
	assertFullCompactFailedReplacementPreserved(t, path)
}

func TestFullCompactCommittedRecoveryAllowsNormalWritesAfterVerification(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("open committed replacement: %v", err)
	}
	if _, err := store.Handle().Exec(`insert into compact_payload values (randomblob(4096))`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Handle().Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	options.FailStep = nil

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatalf("resume committed cleanup after normal write: %v", err)
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var rows int
	if err := database.QueryRow(`select count(*) from compact_payload`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 17 {
		t.Fatalf("payload rows = %d, want 17", rows)
	}
	assertNoFullCompactRecoveryFiles(t, path)
}

func TestFullCompactRecoveryPreservesWorkingCopyAfterProcessCrash(t *testing.T) {
	if os.Getenv(fullCompactCrashChild) == "1" {
		options := fullCompactApplyOptions(os.Getenv("AGENT_GATE_FULL_COMPACT_CRASH_PATH"))
		options.RuntimeDirectory = os.Getenv("AGENT_GATE_FULL_COMPACT_CRASH_RUNTIME")
		options.FailStep = func(step string) error {
			if step == "after-journal:original-renamed" {
				os.Exit(91)
			}
			return nil
		}
		_, _ = auditmaintenance.ApplyFullCompact(t.Context(), options)
		os.Exit(92)
	}
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	command := exec.CommandContext(
		t.Context(), os.Args[0],
		"-test.run=^TestFullCompactRecoveryPreservesWorkingCopyAfterProcessCrash$",
	)
	command.Env = append(
		os.Environ(),
		fullCompactCrashChild+"=1",
		"AGENT_GATE_FULL_COMPACT_CRASH_PATH="+path,
		"AGENT_GATE_FULL_COMPACT_CRASH_RUNTIME="+options.RuntimeDirectory,
	)
	if err := command.Run(); err == nil {
		t.Fatal("crash subprocess error = nil, want exit 91")
	} else {
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 91 {
			t.Fatalf("crash subprocess error = %v, want exit 91", err)
		}
	}
	journal, exists, err := auditstorage.ReadCutoverJournal(path)
	if err != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want crash journal", exists, err)
	}
	workingBytes, err := os.ReadFile(journal.WorkingPath)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatalf("recover crashed full compaction: %v", err)
	}
	failedBytes, err := os.ReadFile(journal.FailedPath)
	if err != nil {
		t.Fatalf("read preserved working copy: %v", err)
	}
	if !bytes.Equal(failedBytes, workingBytes) {
		t.Fatal("preserved working copy bytes changed")
	}
	if _, err := os.Stat(journal.WorkingPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("working copy stat error = %v, want renamed", err)
	}
	if _, exists, err := auditstorage.ReadCutoverJournal(path); err != nil || exists {
		t.Fatalf("journal exists/error = %t/%v, want recovered", exists, err)
	}
}

func TestFullCompactCleanupRejectsSkippedExistingArtifacts(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	journal, exists, err := auditstorage.ReadCutoverJournal(path)
	if err != nil || !exists {
		t.Fatalf("journal exists/error = %t/%v, want committed journal", exists, err)
	}
	journal.Phase = auditstorage.CutoverFailedCleaned
	if err := auditstorage.WriteCutoverJournal(journal); err != nil {
		t.Fatal(err)
	}
	before := snapshotFullCompactDirectory(t, filepath.Dir(path))
	options.FailStep = nil

	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want skipped artifact rejection")
	}
	after := snapshotFullCompactDirectory(t, filepath.Dir(path))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("skipped artifact rejection changed files: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactRecoversInterruptionAtEveryPrecommitPhase(t *testing.T) {
	phases := []auditstorage.CutoverPhase{
		auditstorage.CutoverPrepared,
		auditstorage.CutoverOriginalRenaming,
		auditstorage.CutoverOriginalRenamed,
		auditstorage.CutoverWALMoving,
		auditstorage.CutoverWALMoved,
		auditstorage.CutoverSHMMoving,
		auditstorage.CutoverSHMMoved,
		auditstorage.CutoverInstalling,
		auditstorage.CutoverInstalled,
	}
	for _, phase := range phases {
		for _, boundary := range []string{"before-journal:", "after-journal:"} {
			hook := boundary + string(phase)
			t.Run(hook, func(t *testing.T) {
				assertPrecommitFullCompactInterruption(t, hook)
			})
		}
	}
	for _, hook := range []string{
		"before-rename:original", "after-rename:original",
		"before-rename:sidecar-wal", "after-rename:sidecar-wal",
		"before-rename:sidecar-shm", "after-rename:sidecar-shm",
		"before-rename:replacement", "after-rename:replacement",
		"before-journal:committed",
	} {
		t.Run(hook, func(t *testing.T) {
			assertPrecommitFullCompactInterruption(t, hook)
		})
	}
}

func TestFullCompactRecoversInterruptionAtEveryRollbackStep(t *testing.T) {
	for _, phase := range []auditstorage.CutoverPhase{
		auditstorage.CutoverRestoringReplacement,
		auditstorage.CutoverReplacementPreserved,
		auditstorage.CutoverRestoringOriginal,
		auditstorage.CutoverOriginalRestored,
		auditstorage.CutoverRestoringWAL,
		auditstorage.CutoverWALRestored,
		auditstorage.CutoverRestoringSHM,
		auditstorage.CutoverSHMRestored,
		auditstorage.CutoverRestored,
	} {
		for _, boundary := range []string{"before-journal:", "after-journal:"} {
			hook := boundary + string(phase)
			t.Run(hook, func(t *testing.T) {
				assertRollbackFullCompactInterruption(t, hook)
			})
		}
	}
	for _, hook := range []string{
		"before-rename:recovery-replacement",
		"after-rename:recovery-replacement",
		"before-rename:recovery-original",
		"after-rename:recovery-original",
		"before-rename:recovery-sidecar-wal",
		"after-rename:recovery-sidecar-wal",
		"before-rename:recovery-sidecar-shm",
		"after-rename:recovery-sidecar-shm",
		"before-remove:journal",
	} {
		t.Run(hook, func(t *testing.T) {
			assertRollbackFullCompactInterruption(t, hook)
		})
	}
}

func TestFullCompactNeverRollsBackAfterCommitAtEveryCleanupPhase(t *testing.T) {
	phases := []auditstorage.CutoverPhase{
		auditstorage.CutoverCommitted,
		auditstorage.CutoverCleaningRollback,
		auditstorage.CutoverRollbackCleaned,
		auditstorage.CutoverCleaningWAL,
		auditstorage.CutoverWALCleaned,
		auditstorage.CutoverCleaningSHM,
		auditstorage.CutoverSHMCleaned,
		auditstorage.CutoverCleaningWorking,
		auditstorage.CutoverWorkingCleaned,
		auditstorage.CutoverCleaningFailed,
		auditstorage.CutoverFailedCleaned,
		auditstorage.CutoverCleaningJournal,
	}
	for _, phase := range phases {
		prefixes := []string{"after-journal:"}
		phaseName := string(phase)
		if phase != auditstorage.CutoverCommitted &&
			phase != auditstorage.CutoverCleaningJournal &&
			!strings.HasSuffix(phaseName, "-complete") {
			prefixes = []string{"before-journal:", "after-journal:", "before-remove:", "after-remove:"}
		} else if phase != auditstorage.CutoverCommitted {
			prefixes = []string{"before-journal:", "after-journal:"}
		}
		for _, prefix := range prefixes {
			hook := prefix + string(phase)
			t.Run(hook, func(t *testing.T) {
				assertCommittedFullCompactInterruption(t, hook)
			})
		}
	}
	for _, hook := range []string{"before-remove:journal", "after-remove:journal"} {
		t.Run(hook, func(t *testing.T) {
			assertCommittedFullCompactInterruption(t, hook)
		})
	}
}

func TestFullCompactRecoveryRejectsUnexpectedCommittedIdentityWithoutMutation(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	unexpectedPath := path + ".unexpected"
	if err := os.Rename(path, unexpectedPath); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(unexpectedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	before := snapshotFullCompactDirectory(t, filepath.Dir(path))
	options.FailStep = nil

	_, err = auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("recovery error = %v, want identity rejection", err)
	}
	after := snapshotFullCompactDirectory(t, filepath.Dir(path))
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("identity rejection changed files: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactCommittedCleanupRejectsWrongRollbackIdentityWithoutRemoval(t *testing.T) {
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt("after-journal:committed")
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	journal, exists, err := auditstorage.ReadCutoverJournal(path)
	if err != nil || !exists {
		t.Fatalf("ReadCutoverJournal exists/error = %t/%v", exists, err)
	}
	if err := os.WriteFile(journal.RollbackPath, []byte("wrong rollback"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(journal.RollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	options.FailStep = nil

	_, err = auditmaintenance.ApplyFullCompact(t.Context(), options)

	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("ApplyFullCompact error = %v, want cleanup identity rejection", err)
	}
	after, readErr := os.ReadFile(journal.RollbackPath)
	if readErr != nil {
		t.Fatalf("rollback file was removed: %v", readErr)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("rollback file changed during identity rejection")
	}
}

func assertPrecommitFullCompactInterruption(t *testing.T, hook string) {
	t.Helper()
	path := createFullCompactApplyDatabase(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt(hook)
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want interruption")
	}
	assertFullCompactDatabase(t, path)
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("precommit recovery did not restore the original database inode")
	}
	assertFullCompactFailedReplacementPreserved(t, path)
}

func assertCommittedFullCompactInterruption(t *testing.T, hook string) {
	t.Helper()
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	options.FailStep = failFullCompactAt(hook)
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want committed interruption")
	}
	assertFullCompactDatabase(t, path)
	assertFullCompactMode(t, path, 2)
	options.FailStep = nil
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatalf("resume committed cleanup: %v", err)
	}
	assertFullCompactDatabase(t, path)
	assertFullCompactMode(t, path, 2)
	assertNoFullCompactRecoveryFiles(t, path)
}

func assertRollbackFullCompactInterruption(t *testing.T, hook string) {
	t.Helper()
	path := createFullCompactApplyDatabase(t)
	options := fullCompactApplyOptions(path)
	cutoverInterrupted := false
	rollbackInterrupted := false
	options.FailStep = func(step string) error {
		if step == "after-journal:replacement-installed" && !cutoverInterrupted {
			cutoverInterrupted = true
			return errors.New("start rollback")
		}
		if step == hook && !rollbackInterrupted {
			rollbackInterrupted = true
			return errors.New("interrupt rollback")
		}
		return nil
	}
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err == nil {
		t.Fatal("ApplyFullCompact error = nil, want rollback interruption")
	}
	if !rollbackInterrupted {
		t.Fatalf("rollback hook %q did not run", hook)
	}
	options.FailStep = nil
	if _, err := auditmaintenance.ApplyFullCompact(t.Context(), options); err != nil {
		t.Fatalf("resume rollback: %v", err)
	}
	assertFullCompactDatabase(t, path)
}

func failFullCompactAt(hook string) func(string) error {
	return func(step string) error {
		if step == hook {
			return errors.New("injected interruption")
		}
		return nil
	}
}

func fullCompactApplyOptions(path string) auditmaintenance.FullCompactApplyOptions {
	return auditmaintenance.FullCompactApplyOptions{
		Path:             path,
		RuntimeDirectory: filepath.Join(filepath.Dir(path), "runtime"),
		Owner:            "full-compact-test",
		LeaseTTL:         time.Minute,
		InspectService: func(context.Context) (installer.ServiceState, error) {
			return installer.ServiceState{
				Platform: "launchd", Managed: true, Running: false, BinaryPath: "/opt/agent-gate",
			}, nil
		},
		FreeBytes: func(string) (uint64, error) { return 1 << 40, nil },
		FailStep:  nil,
		Now:       time.Now,
	}
}

func createFullCompactApplyDatabase(t *testing.T) string {
	return createFullCompactApplyDatabaseWithMode(t, 2)
}

func createFullCompactApplyDatabaseWithMode(t *testing.T, mode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	if mode != 2 {
		database, err := sql.Open("sqlite3", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec("pragma auto_vacuum = " + strconv.Itoa(mode)); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`create table legacy_source(value text)`); err != nil {
			t.Fatal(err)
		}
		if err := database.Close(); err != nil {
			t.Fatal(err)
		}
	}
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Handle().Exec(`create table compact_payload(value blob not null)`); err != nil {
		t.Fatal(err)
	}
	for range 256 {
		if _, err := store.Handle().Exec(`insert into compact_payload values (randomblob(4096))`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Handle().Exec(`delete from compact_payload where rowid <= 240`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Handle().Exec(`pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func createFullCompactSwapDatabase(t *testing.T, path string) ([]byte, int) {
	t.Helper()
	swapPath := path + ".swap"
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(swapPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite3", swapPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`insert into compact_payload values (randomblob(4096))`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	swappedBytes, err := os.ReadFile(swapPath)
	if err != nil {
		t.Fatal(err)
	}
	return swappedBytes, 17
}

func assertFullCompactSwapPreserved(t *testing.T, path string, wantBytes []byte, wantRows int) {
	t.Helper()
	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Fatal("canonical swapped database bytes changed")
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var rows int
	if err := database.QueryRow(`select count(*) from compact_payload`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != wantRows {
		t.Fatalf("swapped payload rows = %d, want %d", rows, wantRows)
	}
}

func assertFullCompactDatabase(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var integrity string
	if err := database.QueryRow(`pragma integrity_check`).Scan(&integrity); err != nil {
		t.Fatal(err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity = %q, want ok", integrity)
	}
	foreignKeys, err := database.Query(`pragma foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = foreignKeys.Close() }()
	if foreignKeys.Next() {
		t.Fatal("foreign key check returned a violation")
	}
	if err := foreignKeys.Err(); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := database.QueryRow(`select count(*) from compact_payload`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 16 {
		t.Fatalf("payload rows = %d, want 16", rows)
	}
}

func assertFullCompactMode(t *testing.T, path string, want int) {
	t.Helper()
	mode := readFullCompactMode(t, path)
	if mode != want {
		t.Fatalf("auto-vacuum mode = %d, want %d", mode, want)
	}
}

func readFullCompactMode(t *testing.T, path string) int {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	var mode int
	if err := database.QueryRow(`pragma auto_vacuum`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	return mode
}

func assertNoFullCompactRecoveryFiles(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(path + ".compact.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("recovery files remain: %v", matches)
	}
}

func assertFullCompactFailedReplacementPreserved(t *testing.T, path string) {
	t.Helper()
	matches, err := filepath.Glob(path + ".compact.*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || !strings.HasSuffix(matches[0], ".failed") {
		t.Fatalf("recovery files = %v, want one preserved failed replacement", matches)
	}
}

func snapshotFullCompactDirectory(t *testing.T, directory string) map[string][32]byte {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][32]byte)
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		content, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		result[entry.Name()] = sha256.Sum256(content)
	}
	return result
}
