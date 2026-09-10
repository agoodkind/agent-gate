package installer

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"
)

// ProcessIdentity binds a PID to its kernel start identity and executable.
type ProcessIdentity struct {
	PID        int
	Start      string
	Executable string
}

var errProcessExiting = errors.New("kernel process exit is in progress")

// ProcessControl restricts reset signals to an inspected process identity.
type ProcessControl interface {
	Inspect(int) (ProcessIdentity, error)
	Signal(ProcessIdentity, syscall.Signal) error
}

// NativeProcessControl inspects kernel process identities before each signal.
type NativeProcessControl struct{}

// Inspect reads the kernel identity of one process.
func (NativeProcessControl) Inspect(pid int) (ProcessIdentity, error) { return inspectProcess(pid) }

// Signal rechecks all identity fields immediately before signaling a PID.
func (control NativeProcessControl) Signal(expected ProcessIdentity, signal syscall.Signal) error {
	current, err := control.Inspect(expected.PID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if errors.Is(err, errProcessExiting) && current.PID == expected.PID && current.Start == expected.Start {
		return nil
	}
	if err != nil {
		return resetFailure("inspect reset process", err)
	}
	if current != expected {
		return errors.New("daemon process identity changed before signal")
	}
	if err := syscall.Kill(expected.PID, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return resetFailure("signal verified daemon", err)
	}
	return nil
}

func awaitProcessExit(ctx context.Context, control ProcessControl, expected ProcessIdentity, timeout time.Duration) (bool, error) {
	wait, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		current, err := control.Inspect(expected.PID)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		exiting := errors.Is(err, errProcessExiting) && current.PID == expected.PID && current.Start == expected.Start
		if err != nil && !exiting {
			return false, resetFailure("inspect reset process", err)
		}
		if !exiting && current != expected {
			return false, errors.New("daemon process identity changed during shutdown")
		}
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return false, resetFailure("wait for daemon exit", ctx.Err())
			}
			return false, nil
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func stopResetProcess(ctx context.Context, control ProcessControl, identity ProcessIdentity, timeout time.Duration) error {
	for _, signal := range []syscall.Signal{0, syscall.SIGTERM, syscall.SIGKILL} {
		if signal != 0 {
			if err := control.Signal(identity, signal); err != nil {
				return resetFailure("inspect reset process", err)
			}
		}
		exited, err := awaitProcessExit(ctx, control, identity, timeout)
		if err != nil {
			return resetFailure("inspect reset process", err)
		}
		if exited {
			return nil
		}
	}
	return errors.New("verified daemon did not exit after KILL")
}
