package installer

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

func inspectProcess(pid int) (ProcessIdentity, error) {
	if pid <= 1 {
		return ProcessIdentity{}, errors.New("refuse system process identity")
	}
	base := fmt.Sprintf("/proc/%d", pid)
	data, err := os.ReadFile(base + "/stat")
	if err != nil {
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return ProcessIdentity{}, errors.New("invalid kernel process stat")
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) < 20 {
		return ProcessIdentity{}, errors.New("incomplete kernel process stat")
	}
	if fields[0] == "Z" {
		return ProcessIdentity{}, os.ErrNotExist
	}
	executable, err := os.Readlink(base + "/exe")
	if err != nil {
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	canonical, err := CanonicalExecutablePath(executable)
	if err != nil {
		return ProcessIdentity{}, resetFailure("inspect reset process", err)
	}
	return ProcessIdentity{PID: pid, Start: fields[19], Executable: canonical}, nil
}
