package main_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/daemon"
)

func TestReportFailOpenHistoryRollingWindows(t *testing.T) {
	binaryPath := startStatusTestDaemon(t)
	for _, test := range []struct {
		name      string
		ages      []time.Duration
		truncated bool
		want      string
	}{
		{
			name: "empty",
			want: "1hr: 0, 24hr: 0, 7day: 0, 90day: 0",
		},
		{
			name: "recent and expired records",
			ages: []time.Duration{time.Minute, 2 * time.Hour, 2 * 24 * time.Hour,
				10 * 24 * time.Hour, 91 * 24 * time.Hour, -time.Hour},
			want: "1hr: 1, 24hr: 2, 7day: 3, 90day: 4",
		},
		{
			name: "expired records",
			ages: []time.Duration{91 * 24 * time.Hour},
			want: "1hr: 0, 24hr: 0, 7day: 0, 90day: 0",
		},
		{
			name:      "truncated history",
			truncated: true,
			want:      "1hr: at least 0, 24hr: at least 0, 7day: at least 0, 90day: at least 0",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("XDG_STATE_HOME", t.TempDir())
			path := daemon.FailOpenRecordPath()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			var records bytes.Buffer
			encoder := json.NewEncoder(&records)
			for _, age := range test.ages {
				record := daemon.FailOpenRecord{
					At:     time.Now().Add(-age).In(time.FixedZone("offset", -7*60*60)).Format(time.RFC3339Nano),
					Reason: "daemon_unavailable",
				}
				if err := encoder.Encode(record); err != nil {
					t.Fatal(err)
				}
			}
			if len(test.ages) > 0 {
				if err := os.WriteFile(path, records.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if test.truncated {
				marker := filepath.Join(filepath.Dir(path), "fail-open.truncated")
				if err := os.WriteFile(marker, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := exec.CommandContext(t.Context(), binaryPath, "daemon", "status")
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("daemon status: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), "unevaluated:      "+test.want+" calls allowed without enforcement\n") {
				t.Fatalf("status output = %q, want counts %q", output, test.want)
			}
			if test.truncated && !strings.Contains(string(output), "later calls went unrecorded") {
				t.Fatalf("status output omits the truncation warning: %q", output)
			}
		})
	}
}

func startStatusTestDaemon(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "ag-status-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	binaryPath := filepath.Join(root, "status-test")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v\n%s", err, output)
	}
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", root)
	configurationDirectory := filepath.Join(root, "agent-gate")
	if err := os.MkdirAll(configurationDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configurationDirectory, "config.toml"),
		[]byte("[log]\nlevel = \"error\"\n[update]\nenabled = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	process := exec.CommandContext(t.Context(), binaryPath, "daemon")
	process.Stderr = os.Stderr
	if err := process.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = process.Process.Signal(os.Interrupt)
		_ = process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		status := exec.CommandContext(t.Context(), binaryPath, "daemon", "status")
		output, err := status.CombinedOutput()
		if err == nil {
			return binaryPath
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon startup: %v\n%s", err, output)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
