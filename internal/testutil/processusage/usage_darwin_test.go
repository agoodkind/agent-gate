//go:build auditqualification

package processusage_test

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/testutil/processusage"
)

func TestCountersAgainstCPUAndDiskControls(t *testing.T) {
	var before, after syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &before); err != nil {
		t.Fatal(err)
	}
	start, err := processusage.Read()
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 16*1024*1024)
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		data[0] = sha256.Sum256(data)[0]
	}
	finish, err := processusage.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &after); err != nil {
		t.Fatal(err)
	}
	control := after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano()
	measured := int64(finish.CPUNanoseconds - start.CPUNanoseconds)
	if measured < control*9/10 || measured > control*11/10 {
		t.Fatalf("CPU counter=%d ns, getrusage=%d ns", measured, control)
	}
	file, err := os.Create(filepath.Join(t.TempDir(), "disk-control"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	start, err = processusage.Read()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	finish, err = processusage.Read()
	if err != nil {
		t.Fatal(err)
	}
	written := finish.DiskBytesWritten - start.DiskBytesWritten
	if written < uint64(len(data)) {
		t.Fatalf("physical write bytes=%d, control=%d", written, len(data))
	}
	t.Logf("CPU=%d ns getrusage=%d ns Mach=%d/%d disk=%d bytes", measured, control, finish.TimebaseNumer, finish.TimebaseDenom, written)
}
