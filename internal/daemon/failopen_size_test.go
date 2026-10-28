package daemon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/logstorage"
)

func TestRecordFailOpenPrunesExistingOversizedEvidence(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", "first")
	path := daemon.FailOpenRecordPath()
	file, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(strings.Repeat("x", logstorage.FailOpenMaxBytes+1)); err != nil {
		t.Fatal(err)
	}
	daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", "after pruning")
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) || after.Size() > logstorage.FailOpenMaxBytes {
		t.Fatalf("pruning changed the inode or exceeded the cap: %v", after)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("records = %d, want the earliest record and the new record", len(lines))
	}
	for index, expected := range []string{"first", "after pruning"} {
		var record daemon.FailOpenRecord
		if err := json.Unmarshal(lines[index], &record); err != nil || record.Detail != expected {
			t.Fatalf("record %d = %q, %v", index, record.Detail, err)
		}
	}
	summary, err := daemon.FailOpenRecordSummary()
	if err != nil || !summary.Truncated || summary.Count != 2 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
}

func TestRecordFailOpenConcurrentMaintenanceAndAppends(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", "initial")
	var workers sync.WaitGroup
	const workerCount = 16
	const recordsPerWorker = 20
	maintenanceErrors := make(chan error, recordsPerWorker)
	workers.Go(func() {
		for range recordsPerWorker {
			if err := logstorage.Maintain(config.DefaultStateDir()); err != nil {
				maintenanceErrors <- err
			}
		}
	})
	for worker := range workerCount {
		workers.Go(func() {
			for record := range recordsPerWorker {
				daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", fmt.Sprintf("%d:%d", worker, record))
			}
		})
	}
	workers.Wait()
	close(maintenanceErrors)
	for err := range maintenanceErrors {
		t.Error(err)
	}
	summary, err := daemon.FailOpenRecordSummary()
	if err != nil || summary.Count != 1+workerCount*recordsPerWorker || summary.Truncated {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	data, err := os.ReadFile(daemon.FailOpenRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	details := make(map[string]bool)
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		var record daemon.FailOpenRecord
		if err := json.Unmarshal(line, &record); err != nil {
			t.Fatal(err)
		}
		if details[record.Detail] {
			t.Fatalf("duplicate record %q", record.Detail)
		}
		details[record.Detail] = true
	}
	if _, err := os.Stat(filepath.Join(config.DefaultStateDir(), logstorage.FailOpenTruncatedName)); !os.IsNotExist(err) {
		t.Fatalf("concurrent writes unexpectedly created a truncation marker: %v", err)
	}
}

func TestRecordFailOpenConcurrentAppendsRespectCap(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", "earliest")
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 64 {
				daemon.RecordFailOpen("daemon_unavailable", "claude", "PreToolUse", "Bash", "/repo", strings.Repeat("x", 4096))
			}
		})
	}
	workers.Wait()
	info, err := os.Stat(daemon.FailOpenRecordPath())
	if err != nil || info.Size() > logstorage.FailOpenMaxBytes {
		t.Fatalf("concurrent record size exceeds the cap: %v, %v", info, err)
	}
	summary, err := daemon.FailOpenRecordSummary()
	if err != nil || !summary.Truncated || summary.Count < 2 || summary.Count >= 1025 {
		t.Fatalf("summary = %+v, %v", summary, err)
	}
	data, err := os.ReadFile(daemon.FailOpenRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	for _, line := range lines {
		if !json.Valid(line) {
			t.Fatal("concurrent appends produced an incomplete record")
		}
	}
	var earliest daemon.FailOpenRecord
	if err := json.Unmarshal(lines[0], &earliest); err != nil || earliest.Detail != "earliest" {
		t.Fatalf("earliest record = %+v, %v", earliest, err)
	}
}
