package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ApplyDefaults atomically replaces the configuration from a prepared plan.
//
// The plan retains validated bytes and file identities. Each plan supports one
// application and checks those identities before committing the replacement.
//
//	path, err := ApplyDefaults(plan)
func ApplyDefaults(plan *DefaultsPlan) (string, error) {
	slog.Info("config apply defaults", "has_plan", plan != nil)
	if err := validateDefaultsPlan(plan); err != nil {
		return "", err
	}

	if err := beginDefaultsPlanUse(plan); err != nil {
		return plan.path, err
	}

	defer plan.applyMutex.Unlock()
	defer plan.pathState.closeHandles()
	applyPath := plan.applyPath
	if applyPath == "" {
		applyPath = plan.path
	}

	if err := validateDefaultsApplyPath(plan.path, plan.pathState); err != nil {
		return plan.path, err
	}

	parentPath := filepath.Dir(applyPath)
	if err := os.MkdirAll(parentPath, configDirectoryMode); err != nil {
		return plan.path, reportDefaultsApplyError("create config dir", err)
	}

	writePathState, err := validatePreparedDefaultsApplyPath(plan.path, plan.pathState)
	if err != nil {
		return plan.path, err
	}

	directory, err := openDefaultsApplyDirectory(parentPath, writePathState)
	if err != nil {
		return plan.path, err
	}

	defer func() { _ = directory.Close() }()
	tempFile, tempName, err := createDefaultsTempFile(directory, filepath.Base(applyPath))
	if err != nil {
		return plan.path, reportDefaultsApplyError("create config temp file", err)
	}

	removeTemp := true
	defer func() {
		if removeTemp {
			_ = unix.Unlinkat(int(directory.Fd()), tempName, 0)
		}
	}()
	if err := writeDefaultsTempFile(
		tempFile,
		defaultsApplyFileMode(writePathState),
		plan.content,
	); err != nil {
		return plan.path, err
	}

	if err := validateDefaultsApplyPath(plan.path, writePathState); err != nil {
		return plan.path, err
	}

	if plan.beforeRename != nil {
		plan.beforeRename()
	}

	replacement, cleanupTemp, err := beginDefaultsTargetReplacement(
		directory,
		tempName,
		filepath.Base(applyPath),
		defaultsPathInfo(writePathState, applyPath),
	)
	removeTemp = cleanupTemp
	if err != nil {
		return plan.path, err
	}

	if err := validateDefaultsApplyParents(plan.path, writePathState); err != nil {
		rollbackErr := replacement.rollback()
		joinedErr := errors.Join(err, rollbackErr)
		slog.Warn("config replacement rejected", "err", joinedErr)

		return plan.path, joinedErr
	}

	if err := replacement.commit(); err != nil {
		return plan.path, err
	}

	if err := directory.Sync(); err != nil {
		return plan.path, reportDefaultsApplyError("synchronize config directory", err)
	}

	return plan.path, nil
}

func writeDefaultsTempFile(tempFile *os.File, fileMode os.FileMode, content []byte) error {
	if err := tempFile.Chmod(fileMode); err != nil {
		_ = tempFile.Close()

		return reportDefaultsApplyError("set config temp file mode", err)
	}

	if _, err := tempFile.Write(content); err != nil {
		_ = tempFile.Close()

		return reportDefaultsApplyError("write config temp file", err)
	}

	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()

		return reportDefaultsApplyError("synchronize config temp file", err)
	}

	if err := tempFile.Close(); err != nil {
		return reportDefaultsApplyError("close config temp file", err)
	}

	return nil
}
