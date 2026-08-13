package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"golang.org/x/sys/unix"
)

type defaultsTargetReplacement struct {
	directory  *os.File
	tempName   string
	targetName string
	hadTarget  bool
}

func beginDefaultsTargetReplacement(
	directory *os.File,
	tempName string,
	targetName string,
	expected *defaultsPathComponent,
) (*defaultsTargetReplacement, bool, error) {
	directoryDescriptor := int(directory.Fd())
	if expected == nil {
		if err := renameDefaultsNoReplace(
			directoryDescriptor,
			tempName,
			targetName,
		); err != nil {
			return nil, true, reportDefaultsApplyError("replace config", err)
		}
		return &defaultsTargetReplacement{
			directory:  directory,
			tempName:   tempName,
			targetName: targetName,
			hadTarget:  false,
		}, false, nil
	}
	if err := exchangeDefaultsNames(
		directoryDescriptor,
		tempName,
		targetName,
	); err != nil {
		return nil, true, reportDefaultsApplyError("replace config", err)
	}
	displaced, err := openDefaultsDirectoryEntry(directoryDescriptor, tempName)
	if err == nil {
		defer func() { _ = displaced.Close() }()
		var displacedInfo os.FileInfo
		displacedInfo, err = displaced.Stat()
		var displacedBirthTime defaultsBirthTime
		if err == nil {
			displacedBirthTime, err = captureDefaultsBirthTimeAt(
				directoryDescriptor,
				tempName,
			)
		}
		if err == nil && (!os.SameFile(expected.info, displacedInfo) ||
			!sameDefaultsBirthTime(expected.birthTime, displacedBirthTime)) {
			err = errors.New("config target identity changed before replacement")
		}
	}
	if err != nil {
		if rollbackErr := exchangeDefaultsNames(
			directoryDescriptor,
			tempName,
			targetName,
		); rollbackErr != nil {
			return nil, false, reportDefaultsApplyError(
				"restore changed config target",
				errors.Join(err, rollbackErr),
			)
		}
		cleanupErr := unix.Unlinkat(directoryDescriptor, tempName, 0)
		syncErr := directory.Sync()
		return nil, false, reportDefaultsApplyError(
			"replace config",
			errors.Join(err, cleanupErr, syncErr),
		)
	}
	return &defaultsTargetReplacement{
		directory:  directory,
		tempName:   tempName,
		targetName: targetName,
		hadTarget:  true,
	}, false, nil
}

func (replacement *defaultsTargetReplacement) commit() error {
	if replacement == nil || !replacement.hadTarget {
		return nil
	}
	if err := unix.Unlinkat(
		int(replacement.directory.Fd()),
		replacement.tempName,
		0,
	); err != nil {
		return reportDefaultsApplyError("remove replaced config", err)
	}
	return nil
}

func (replacement *defaultsTargetReplacement) rollback() error {
	if replacement == nil {
		return nil
	}
	directoryDescriptor := int(replacement.directory.Fd())
	if replacement.hadTarget {
		if err := exchangeDefaultsNames(
			directoryDescriptor,
			replacement.tempName,
			replacement.targetName,
		); err != nil {
			return reportDefaultsApplyError("restore config target", err)
		}
		if err := unix.Unlinkat(directoryDescriptor, replacement.tempName, 0); err != nil {
			return reportDefaultsApplyError("remove rejected config", err)
		}
	} else if err := unix.Unlinkat(
		directoryDescriptor,
		replacement.targetName,
		0,
	); err != nil {
		return reportDefaultsApplyError("remove rejected config", err)
	}
	if err := replacement.directory.Sync(); err != nil {
		return reportDefaultsApplyError("synchronize restored config directory", err)
	}
	return nil
}

func openDefaultsDirectoryEntry(directoryDescriptor int, name string) (*os.File, error) {
	fileDescriptor, err := unix.Openat(
		directoryDescriptor,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		wrappedErr := fmt.Errorf("open displaced config target: %w", err)
		slog.Warn("open displaced config target failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	return os.NewFile(uintptr(fileDescriptor), name), nil
}

func defaultsApplyFileMode(pathState defaultsPathState) os.FileMode {
	component := defaultsPathInfo(pathState, pathState.applyPath)
	if component == nil {
		return configFileMode
	}
	return component.info.Mode().Perm()
}

func defaultsPathInfo(
	pathState defaultsPathState,
	path string,
) *defaultsPathComponent {
	for _, component := range slices.Backward(pathState.components) {
		if component.path == path && component.exists {
			matched := component
			return &matched
		}
	}
	return nil
}

func retainDefaultsPathHandles(pathState *defaultsPathState) error {
	pathState.handles = make(map[string]*os.File)
	for _, component := range pathState.components {
		if !component.exists || pathState.handles[component.path] != nil {
			continue
		}
		handle, err := openDefaultsIdentityHandle(
			component.path,
			component.info.Mode()&os.ModeSymlink != 0,
		)
		if err != nil {
			pathState.closeHandles()
			return reportDefaultsPreparationError("retain config path identity", err)
		}
		handleInfo, err := handle.Stat()
		if err != nil || !os.SameFile(component.info, handleInfo) {
			_ = handle.Close()
			pathState.closeHandles()
			if err == nil {
				err = errors.New("config path identity changed while retaining it")
			}
			return reportDefaultsPreparationError("retain config path identity", err)
		}
		pathState.handles[component.path] = handle
	}
	return nil
}

func closeDefaultsPlanHandles(plan *DefaultsPlan) {
	_ = plan.Close()
}

func (pathState *defaultsPathState) closeHandles() {
	for path, handle := range pathState.handles {
		if err := handle.Close(); err != nil {
			slog.Warn("close config identity handle failed", "path", path, "err", err)
		}
	}
	pathState.handles = nil
}
