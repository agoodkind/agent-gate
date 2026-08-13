//go:build !unix

// Package processlock owns the daemon's cross-process exclusion lock.
package processlock

import "errors"

// ErrBusy reports that another process holds the daemon lock.
var ErrBusy = errors.New("daemon process lock is held")

// Lock is one held daemon process lock.
type Lock struct{}

// Acquire reports that process locking is unsupported.
func Acquire(string) (*Lock, error) {
	return nil, errors.New("daemon process locking is unsupported")
}

// Release unlocks the process lock.
func (lock *Lock) Release() error { return nil }
