//go:build !unix

package auditmaintenance

import "errors"

type maintenanceFileLock struct{}

func acquireFileLock(string) (*maintenanceFileLock, error) {
	return nil, errors.New("audit maintenance apply is unsupported on this platform")
}

func (lock *maintenanceFileLock) release() error {
	return nil
}
