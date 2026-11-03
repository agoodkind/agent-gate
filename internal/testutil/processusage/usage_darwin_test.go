//go:build auditqualification

package processusage_test

import (
	"bufio"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/testutil/processusage"
)

func TestReadPIDMemoryControl(t *testing.T) {
	const allocationBytes = 64 * 1024 * 1024
	if os.Getenv("PROCESSUSAGE_MEMORY_CHILD") == "1" {
		input := bufio.NewReader(os.Stdin)
		for index := range 2 {
			if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
				t.Fatal(err)
			}
			if _, err := input.ReadString('\n'); err != nil {
				t.Fatal(err)
			}
			if index == 0 {
				memory := make([]byte, allocationBytes)
				for offset := 0; offset < len(memory); offset += os.Getpagesize() {
					memory[offset] = 1
				}
				defer runtime.KeepAlive(memory)
			}
		}
		return
	}
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestReadPIDMemoryControl$")
	command.Env = append(os.Environ(), "PROCESSUSAGE_MEMORY_CHILD=1")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	})
	ready := bufio.NewScanner(output)
	read := func() processusage.Snapshot {
		t.Helper()
		if !ready.Scan() || ready.Text() != "ready" {
			t.Fatalf("memory control readiness failed: %s, %v", ready.Text(), ready.Err())
		}
		usage, readError := processusage.ReadPID(command.Process.Pid)
		if readError != nil {
			t.Fatal(readError)
		}
		return usage
	}
	before := read()
	if _, err := fmt.Fprintln(input); err != nil {
		t.Fatal(err)
	}
	after := read()
	for name, delta := range map[string]int64{
		"resident":           int64(after.ResidentBytes) - int64(before.ResidentBytes),
		"physical footprint": int64(after.PhysicalFootprintBytes) - int64(before.PhysicalFootprintBytes),
	} {
		if delta < allocationBytes/2 || delta > allocationBytes*2 {
			t.Fatalf("%s growth=%d bytes, allocation=%d bytes", name, delta, allocationBytes)
		}
		t.Logf("%s growth=%d bytes, allocation=%d bytes", name, delta, allocationBytes)
	}
	if _, err := fmt.Fprintln(input); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestReadPIDRejectsInvalidProcess(t *testing.T) {
	for _, pid := range []int{-1, 0, math.MaxInt32 + 1, math.MaxInt32} {
		_, err := processusage.ReadPID(pid)
		want := syscall.EINVAL
		if pid == math.MaxInt32 {
			want = syscall.ESRCH
		}
		if !errors.Is(err, want) {
			t.Fatalf("ReadPID(%d) error=%v, want=%v", pid, err, want)
		}
	}
}

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
