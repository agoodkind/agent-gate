package daemon_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/daemon"
)

func TestFailOpenRecordSummaryRollingWindows(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	now := time.Now()
	ages := []time.Duration{
		0, time.Hour - time.Minute, time.Hour + time.Minute,
		24*time.Hour - time.Minute, 24*time.Hour + time.Minute,
		7*24*time.Hour - time.Minute, 7*24*time.Hour + time.Minute,
		90*24*time.Hour - time.Minute, 90*24*time.Hour + time.Minute,
		-time.Hour,
	}
	path := daemon.FailOpenRecordPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	encoder := json.NewEncoder(file)
	for _, age := range ages {
		if err := encoder.Encode(daemon.FailOpenRecord{
			At: now.Add(-age).Format(time.RFC3339Nano), Reason: "daemon_unavailable",
		}); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := daemon.FailOpenRecordSummary()
	if err != nil {
		t.Fatal(err)
	}
	if summary.LastHour != 2 || summary.Last24Hours != 4 ||
		summary.Last7Days != 6 || summary.Last90Days != 8 {
		t.Fatalf("rolling counts = %d/%d/%d/%d, want 2/4/6/8",
			summary.LastHour, summary.Last24Hours, summary.Last7Days, summary.Last90Days)
	}
}
