package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/agent-gate/internal/auditstorage"
)

type fullCompactCleanupStep struct {
	before   auditstorage.CutoverPhase
	after    auditstorage.CutoverPhase
	path     string
	expected *auditstorage.FileIdentity
}

func cleanupCommittedFullCompact(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	slog.Debug("clean up committed full compaction", "run_id", journal.RunID)
	steps := []fullCompactCleanupStep{
		{auditstorage.CutoverCleaningRollback, auditstorage.CutoverRollbackCleaned, journal.RollbackPath, &journal.SourceIdentity},
		{auditstorage.CutoverCleaningWAL, auditstorage.CutoverWALCleaned, journal.RollbackPath + "-wal", journal.WALIdentity},
		{auditstorage.CutoverCleaningSHM, auditstorage.CutoverSHMCleaned, journal.RollbackPath + "-shm", journal.SHMIdentity},
		{auditstorage.CutoverCleaningWorking, auditstorage.CutoverWorkingCleaned, journal.WorkingPath, &journal.CopyIdentity},
		{auditstorage.CutoverCleaningFailed, auditstorage.CutoverFailedCleaned, journal.FailedPath, &journal.CopyIdentity},
	}
	start := fullCompactCleanupStart(journal.Phase, steps)
	if journal.Phase == auditstorage.CutoverCleaningJournal {
		start = len(steps)
	}
	for _, step := range steps[:start] {
		if err := verifyFullCompactCleanupCompleted(step.path); err != nil {
			return err
		}
	}
	for _, step := range steps[start:] {
		if err := runFullCompactCleanupStep(journal, step, failStep); err != nil {
			return err
		}
	}
	if err := removeFullCompactJournal(journal, auditstorage.CutoverCleaningJournal, failStep); err != nil {
		return wrapError("remove committed full compaction journal", err)
	}
	return nil
}

func fullCompactCleanupStart(
	phase auditstorage.CutoverPhase,
	steps []fullCompactCleanupStep,
) int {
	for index, step := range steps {
		if phase == step.before {
			return index
		}
		if phase == step.after {
			return index + 1
		}
	}
	return 0
}

func verifyFullCompactCleanupCompleted(path string) error {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return wrapError("inspect completed full compaction cleanup", err)
	}
	return errors.New("full compaction cleanup journal skipped an existing recovery file")
}

func runFullCompactCleanupStep(
	journal *auditstorage.CutoverJournal,
	step fullCompactCleanupStep,
	failStep func(string) error,
) error {
	slog.Debug("run full compaction cleanup step", "phase", step.before, "path", step.path)
	if err := verifyFullCompactCleanupCandidate(step.path, step.expected); err != nil {
		return err
	}
	if err := advanceFullCompactJournal(journal, step.before, failStep); err != nil {
		return err
	}
	if err := failFullCompactHook(failStep, "before-remove:"+string(step.before)); err != nil {
		return err
	}
	if err := os.Remove(step.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return wrapError("remove full compaction recovery file", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize full compaction cleanup", err)
	}
	if err := failFullCompactHook(failStep, "after-remove:"+string(step.before)); err != nil {
		return err
	}
	return advanceFullCompactJournal(journal, step.after, failStep)
}

func verifyFullCompactCleanupCandidate(
	path string,
	expected *auditstorage.FileIdentity,
) error {
	identity, err := fullCompactFileIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if expected == nil || identity != *expected {
		return errors.New("full compaction cleanup file identity does not match journal")
	}
	return nil
}

func removeFullCompactJournal(
	journal *auditstorage.CutoverJournal,
	phase auditstorage.CutoverPhase,
	failStep func(string) error,
) error {
	if err := advanceFullCompactJournal(journal, phase, failStep); err != nil {
		return err
	}
	if err := failFullCompactHook(failStep, "before-remove:journal"); err != nil {
		return err
	}
	if err := auditstorage.RemoveCutoverJournal(journal.DatabasePath); err != nil {
		return wrapError("remove full compaction journal", err)
	}
	return failFullCompactHook(failStep, "after-remove:journal")
}

func releaseFullCompactLease(parent context.Context, path string, lease maintenanceLease) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), fullCompactLeaseCleanupTimeout)
	defer cancel()
	database, err := openFullCompactDatabase(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	return releaseLeaseBounded(ctx, database, lease)
}

func releaseFullCompactSourceAndCopyLeases(
	ctx context.Context,
	database *sql.DB,
	workingPath string,
	lease maintenanceLease,
) error {
	if err := releaseFullCompactLease(ctx, workingPath, lease); err != nil {
		return err
	}
	return releaseLeaseBounded(ctx, database, lease)
}
