//go:build unix

package auditmaintenance

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

type maintenanceFileLock struct {
	file *os.File
}

func acquireFileLock(databasePath string) (*maintenanceFileLock, error) {
	lockPath := databasePath + ".maintenance.lock"
	// #nosec G703 -- the configured audit database owns this exact sibling lock.
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, wrapError("open audit maintenance lock", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrMaintenanceBusy
		}
		return nil, wrapError("acquire audit maintenance lock", err)
	}
	return &maintenanceFileLock{file: file}, nil
}

func (lock *maintenanceFileLock) release() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		_ = lock.file.Close()
		return wrapError("release audit maintenance lock", err)
	}
	if err := lock.file.Close(); err != nil {
		return wrapError("close audit maintenance lock", err)
	}
	return nil
}
