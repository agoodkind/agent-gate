package installer

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// Darwin marks P_WEXIT before argv becomes unavailable and before SZOMB.
const darwinProcessExiting = 0x00002000

func inspectProcess(pid int) (ProcessIdentity, error) {
	if pid <= 1 {
		return ProcessIdentity{}, errors.New("refuse system process identity")
	}
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.ENOENT) {
		return ProcessIdentity{}, os.ErrNotExist
	}
	if err != nil {
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	if len(processes) == 0 {
		return ProcessIdentity{}, os.ErrNotExist
	}
	if len(processes) != 1 || int(processes[0].Proc.P_pid) != pid {
		return ProcessIdentity{}, errors.New("kernel process identity is ambiguous")
	}
	process := processes[0]
	if process.Proc.P_stat == 5 {
		return ProcessIdentity{}, os.ErrNotExist
	}
	identity := ProcessIdentity{PID: pid, Start: fmt.Sprintf("%d:%d", process.Proc.P_starttime.Sec, process.Proc.P_starttime.Usec), Executable: ""}
	if process.Proc.P_flag&darwinProcessExiting != 0 {
		return identity, errProcessExiting
	}
	arguments, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		remaining, lookupErr := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
		if lookupErr == nil && (len(remaining) == 0 || (len(remaining) == 1 && remaining[0].Proc.P_stat == 5)) {
			return ProcessIdentity{}, os.ErrNotExist
		}
		if lookupErr == nil && len(remaining) == 1 && remaining[0].Proc.P_starttime == process.Proc.P_starttime && remaining[0].Proc.P_flag&darwinProcessExiting != 0 {
			return identity, errProcessExiting
		}
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	if len(arguments) < 5 {
		return ProcessIdentity{}, errors.New("kernel process arguments are incomplete")
	}
	executable, _, found := bytes.Cut(arguments[4:], []byte{0})
	if !found || len(executable) == 0 {
		return ProcessIdentity{}, errors.New("kernel executable path is missing")
	}
	canonical, err := CanonicalExecutablePath(string(executable))
	if err != nil {
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	identity.Executable = canonical
	return identity, nil
}
