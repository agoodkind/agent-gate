//go:build unix

// Package processlock owns the daemon's cross-process exclusion lock.
package processlock

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// ErrBusy reports that another process holds the daemon lock.
var ErrBusy = errors.New("daemon process lock is held")

// Lock is one held daemon process lock.
type Lock struct {
	file *os.File
}

// Acquire obtains the same nonblocking lock used by the daemon.
func Acquire(runtimeDirectory string) (*Lock, error) {
	slog.Debug("acquire daemon process lock", "runtime_directory", runtimeDirectory)
	path := filepath.Join(runtimeDirectory, "daemon.process.lock")
	// #nosec G304 -- the runtime directory is selected by trusted configuration.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, reportProcessLockError("open daemon process lock", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrBusy
		}
		return nil, reportProcessLockError("acquire daemon process lock", err)
	}
	return &Lock{file: file}, nil
}

// Release unlocks and closes the process lock.
func (lock *Lock) Release() error {
	slog.Debug("release daemon process lock")
	if lock == nil || lock.file == nil {
		return nil
	}
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		_ = lock.file.Close()
		return reportProcessLockError("release daemon process lock", err)
	}
	if err := lock.file.Close(); err != nil {
		return reportProcessLockError("close daemon process lock", err)
	}
	lock.file = nil
	return nil
}

func reportProcessLockError(action string, err error) error {
	result := fmt.Errorf("%s: %w", action, err)
	slog.Warn(action, "err", result)
	return result
}
