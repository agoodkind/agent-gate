//go:build auditqualification

// Package processusage measures operating-system counters for qualification tests.
package processusage

// #include "usage_darwin.h"
import "C"

import (
	"fmt"
	"math"
	"os"
	"syscall"
)

// Snapshot contains process counters and current memory sizes in bytes.
type Snapshot struct {
	CPUNanoseconds         uint64
	DiskBytesWritten       uint64
	ResidentBytes          uint64
	PhysicalFootprintBytes uint64
	TimebaseNumer          uint32
	TimebaseDenom          uint32
}

// Read converts Darwin Mach CPU ticks and preserves physical disk accounting.
func Read() (Snapshot, error) {
	return ReadPID(os.Getpid())
}

// ReadPID reads one process, including its threads but excluding its children.
func ReadPID(pid int) (Snapshot, error) {
	if pid <= 0 || pid > math.MaxInt32 {
		return Snapshot{}, &os.SyscallError{Syscall: fmt.Sprintf("proc_pid_rusage PID %d", pid), Err: syscall.EINVAL}
	}
	var result C.struct_process_usage
	if status := C.read_process_usage(C.int(pid), &result); status != 0 {
		return Snapshot{}, &os.SyscallError{Syscall: fmt.Sprintf("proc_pid_rusage PID %d", pid), Err: syscall.Errno(status)}
	}
	return Snapshot{CPUNanoseconds: uint64(result.cpu_nanoseconds), DiskBytesWritten: uint64(result.disk_bytes_written), ResidentBytes: uint64(result.resident_bytes), PhysicalFootprintBytes: uint64(result.physical_footprint_bytes), TimebaseNumer: uint32(result.timebase_numer), TimebaseDenom: uint32(result.timebase_denom)}, nil
}
