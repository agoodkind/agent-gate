//go:build auditqualification

// Package processusage measures operating-system counters for qualification tests.
package processusage

// #include "usage_darwin.h"
import "C"

import "fmt"

// Snapshot contains cumulative counters for this process, including its threads.
type Snapshot struct {
	CPUNanoseconds   uint64
	DiskBytesWritten uint64
	TimebaseNumer    uint32
	TimebaseDenom    uint32
}

// Read converts Darwin Mach CPU ticks and preserves physical disk accounting.
func Read() (Snapshot, error) {
	var result C.struct_process_usage
	if C.read_process_usage(&result) != 0 {
		return Snapshot{}, fmt.Errorf("read Darwin process resource usage")
	}
	return Snapshot{CPUNanoseconds: uint64(result.cpu_nanoseconds), DiskBytesWritten: uint64(result.disk_bytes_written), TimebaseNumer: uint32(result.timebase_numer), TimebaseDenom: uint32(result.timebase_denom)}, nil
}
