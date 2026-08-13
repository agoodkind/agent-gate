package auditmaintenance

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func moveFullCompactSidecar(
	journal *auditstorage.CutoverJournal,
	options FullCompactApplyOptions,
	suffix string,
	expected *auditstorage.FileIdentity,
	before auditstorage.CutoverPhase,
	after auditstorage.CutoverPhase,
) error {
	slog.Debug("move full compaction sidecar", "suffix", suffix)
	if err := advanceFullCompactJournal(journal, before, options.FailStep); err != nil {
		return err
	}
	if err := failFullCompactHook(options.FailStep, "before-rename:sidecar"+suffix); err != nil {
		return err
	}
	if err := renameFullCompactSidecar(
		journal.DatabasePath+suffix,
		journal.RollbackPath+suffix,
		expected,
	); err != nil {
		return wrapError("move original audit database sidecar", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize moved audit database sidecar", err)
	}
	if err := failFullCompactHook(options.FailStep, "after-rename:sidecar"+suffix); err != nil {
		return err
	}
	return advanceFullCompactJournal(journal, after, options.FailStep)
}

func restoreFullCompactSidecars(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	slog.Debug("restore full compaction sidecars")
	if err := restoreFullCompactSidecar(
		journal,
		failStep,
		"-wal",
		journal.WALIdentity,
		auditstorage.CutoverRestoringWAL,
		auditstorage.CutoverWALRestored,
	); err != nil {
		return err
	}
	return restoreFullCompactSidecar(
		journal,
		failStep,
		"-shm",
		journal.SHMIdentity,
		auditstorage.CutoverRestoringSHM,
		auditstorage.CutoverSHMRestored,
	)
}

func restoreFullCompactSidecar(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
	suffix string,
	expected *auditstorage.FileIdentity,
	before auditstorage.CutoverPhase,
	after auditstorage.CutoverPhase,
) error {
	slog.Debug("restore full compaction sidecar", "suffix", suffix)
	if err := advanceFullCompactJournal(journal, before, failStep); err != nil {
		return err
	}
	hookSuffix := strings.TrimPrefix(suffix, "-")
	if err := failFullCompactHook(failStep, "before-rename:recovery-sidecar-"+hookSuffix); err != nil {
		return err
	}
	if err := renameFullCompactSidecar(
		journal.RollbackPath+suffix,
		journal.DatabasePath+suffix,
		expected,
	); err != nil {
		return wrapError("restore original audit database sidecar", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize restored audit database sidecar", err)
	}
	if err := failFullCompactHook(failStep, "after-rename:recovery-sidecar-"+hookSuffix); err != nil {
		return err
	}
	return advanceFullCompactJournal(journal, after, failStep)
}

func renameFullCompactSidecar(
	sourcePath string,
	destinationPath string,
	expected *auditstorage.FileIdentity,
) error {
	slog.Debug(
		"rename full compaction sidecar",
		"source", sourcePath,
		"destination", destinationPath,
	)
	sourceIdentity, err := optionalFullCompactFileIdentity(sourcePath)
	if err != nil {
		return err
	}
	destinationIdentity, err := optionalFullCompactFileIdentity(destinationPath)
	if err != nil {
		return err
	}
	if expected == nil {
		if sourceIdentity != nil || destinationIdentity != nil {
			return errors.New("unexpected full compaction sidecar identity")
		}
		return nil
	}
	if sourceIdentity == nil {
		if destinationIdentity != nil && *destinationIdentity == *expected {
			return nil
		}
		return errors.New("recorded full compaction sidecar is unavailable")
	}
	if *sourceIdentity != *expected || destinationIdentity != nil {
		return errors.New("full compaction sidecar identity does not match journal")
	}
	if err := os.Rename(sourcePath, destinationPath); err != nil {
		return wrapError("rename full compaction sidecar", err)
	}
	movedIdentity, err := fullCompactFileIdentity(destinationPath)
	if err != nil {
		return err
	}
	if movedIdentity != *expected {
		return errors.New("renamed full compaction sidecar identity does not match journal")
	}
	return nil
}
