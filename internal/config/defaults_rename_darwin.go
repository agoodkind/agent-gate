//go:build darwin

package config

import (
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

func openDefaultsIdentityHandle(path string, symbolicLink bool) (*os.File, error) {
	flags := unix.O_EVTONLY | unix.O_CLOEXEC
	if symbolicLink {
		flags |= unix.O_SYMLINK
	}
	fileDescriptor, err := unix.Open(path, flags, 0)
	if err != nil {
		wrappedErr := fmt.Errorf("open config identity handle: %w", err)
		slog.Warn("open config identity handle failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	return os.NewFile(uintptr(fileDescriptor), path), nil
}

func captureDefaultsBirthTimeAt(
	directoryDescriptor int,
	path string,
) (defaultsBirthTime, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(
		directoryDescriptor,
		path,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		wrappedErr := fmt.Errorf("inspect config birth time: %w", err)
		slog.Warn("inspect config birth time failed", "err", wrappedErr)
		return defaultsBirthTime{}, wrappedErr
	}
	return defaultsBirthTime{
		seconds:     stat.Btim.Sec,
		nanoseconds: stat.Btim.Nsec,
		available:   true,
	}, nil
}

func exchangeDefaultsNames(directoryDescriptor int, first string, second string) error {
	if err := unix.RenameatxNp(
		directoryDescriptor,
		first,
		directoryDescriptor,
		second,
		unix.RENAME_SWAP,
	); err != nil {
		wrappedErr := fmt.Errorf("exchange config entries: %w", err)
		slog.Warn("exchange config entries failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}

func renameDefaultsNoReplace(directoryDescriptor int, source string, target string) error {
	if err := unix.RenameatxNp(
		directoryDescriptor,
		source,
		directoryDescriptor,
		target,
		unix.RENAME_EXCL,
	); err != nil {
		wrappedErr := fmt.Errorf("install new config entry: %w", err)
		slog.Warn("install new config entry failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}
