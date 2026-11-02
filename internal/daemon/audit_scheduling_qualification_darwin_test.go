//go:build auditqualification

package daemon_test

import (
	"fmt"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/daemon"
)

func TestSchedulingHistoricalWriteMeasurement(t *testing.T) {
	for _, count := range []int{12, 48} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			harness := newOwnershipHarness(t, 1, false, false)
			harness.server.Close()
			bucket := seedRecoveryBacklog(t, harness, count)
			before := qualificationReadUsage(t)
			started := time.Now()
			server, err := daemon.New(t.Context(), ownershipLogger(), harness.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			server.StartAuditScheduler(t.Context())
			for range count {
				call := harness.nextCall(t)
				close(call.release)
			}
			ownershipWait(t, func() bool { complete, err := auditstorage.Completed(bucket.Path); return err == nil && complete })
			finished := qualificationReadUsage(t)
			server.Close()
			closed := qualificationReadUsage(t)
			t.Logf("receipts=%d elapsed=%s startup_recovery_conversion_bytes=%d shutdown_bytes=%d total_bytes=%d cpu_ns=%d", count, time.Since(started), finished.DiskBytesWritten-before.DiskBytesWritten, closed.DiskBytesWritten-finished.DiskBytesWritten, closed.DiskBytesWritten-before.DiskBytesWritten, closed.CPUNanoseconds-before.CPUNanoseconds)
			if outcomes := ownershipOutcomes(t, harness.cfg); outcomes != [4]int{count, count, count, count} {
				t.Fatalf("durable outcomes = %v", outcomes)
			}
		})
	}
}

func TestSchedulingIdleAfterCompletedHistory(t *testing.T) {
	cfg := qualificationIdleConfig(t)
	const history = "160"
	count := qualificationSeedIdleHistory(t, cfg, history, time.Now())
	catalog := qualificationIdleCatalog(t, cfg)
	policy := cfg.AuditStoragePolicy().Rotation()
	before := qualificationReadUsage(t)
	server, err := daemon.New(t.Context(), qualificationIdleLogger(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.StartAuditScheduler(t.Context())
	status, err := catalog.Status(policy, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, file := range status.RetainedFiles {
		if file.Path != status.CurrentBucketPath {
			paths = append(paths, file.Path)
		}
	}
	if len(paths) != count {
		t.Fatalf("retained files = %d, expected %d", len(paths), count)
	}
	ownershipWait(t, func() bool {
		for _, path := range paths {
			complete, err := auditstorage.Completed(path)
			if err != nil {
				t.Fatal(err)
			}
			if !complete {
				return false
			}
		}
		return true
	})
	converted := qualificationReadUsage(t)
	previous := converted
	settled := 0
	for attempt := 0; settled < 2; attempt++ {
		if attempt == 10 {
			t.Fatal("maintenance writes did not settle")
		}
		time.Sleep(time.Second)
		current := qualificationReadUsage(t)
		if current.DiskBytesWritten == previous.DiskBytesWritten {
			settled++
		} else {
			settled = 0
		}
		previous = current
	}
	start := qualificationReadUsage(t)
	time.Sleep(5 * time.Second)
	finish := qualificationReadUsage(t)
	if written := finish.DiskBytesWritten - start.DiskBytesWritten; written != 0 {
		t.Fatalf("idle process wrote %d bytes", written)
	}
	server.Close()
	closed := qualificationReadUsage(t)
	t.Logf("history_files=%d startup_conversion_bytes=%d settling_bytes=%d idle_seconds=5 idle_bytes=%d idle_cpu_ns=%d resident_start=%d resident_end=%d footprint_start=%d footprint_end=%d shutdown_bytes=%d", count, converted.DiskBytesWritten-before.DiskBytesWritten, start.DiskBytesWritten-converted.DiskBytesWritten, finish.DiskBytesWritten-start.DiskBytesWritten, finish.CPUNanoseconds-start.CPUNanoseconds, start.ResidentBytes, finish.ResidentBytes, start.PhysicalFootprintBytes, finish.PhysicalFootprintBytes, closed.DiskBytesWritten-finish.DiskBytesWritten)
}
