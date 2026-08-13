//go:build linux

package config

import (
	"fmt"
	"log/slog"
	"os"

	"golang.org/x/sys/unix"
)

func openDefaultsIdentityHandle(path string, _ bool) (*os.File, error) {
	fileDescriptor, err := unix.Open(
		path,
		unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
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
	var stat unix.Statx_t
	if err := unix.Statx(
		directoryDescriptor,
		path,
		unix.AT_SYMLINK_NOFOLLOW,
		unix.STATX_BTIME,
		&stat,
	); err != nil {
		wrappedErr := fmt.Errorf("inspect config birth time: %w", err)
		slog.Warn("inspect config birth time failed", "err", wrappedErr)
		return defaultsBirthTime{}, wrappedErr
	}
	if stat.Mask&unix.STATX_BTIME == 0 {
		return defaultsBirthTime{
			seconds: 0, nanoseconds: 0, available: false,
		}, nil
	}
	return defaultsBirthTime{
		seconds:     stat.Btime.Sec,
		nanoseconds: int64(stat.Btime.Nsec),
		available:   true,
	}, nil
}

func exchangeDefaultsNames(directoryDescriptor int, first string, second string) error {
	if err := unix.Renameat2(
		directoryDescriptor,
		first,
		directoryDescriptor,
		second,
		unix.RENAME_EXCHANGE,
	); err != nil {
		wrappedErr := fmt.Errorf("exchange config entries: %w", err)
		slog.Warn("exchange config entries failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}

func renameDefaultsNoReplace(directoryDescriptor int, source string, target string) error {
	if err := unix.Renameat2(
		directoryDescriptor,
		source,
		directoryDescriptor,
		target,
		unix.RENAME_NOREPLACE,
	); err != nil {
		wrappedErr := fmt.Errorf("install new config entry: %w", err)
		slog.Warn("install new config entry failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}
