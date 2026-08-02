package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFailOpenLeavesADurableRecord is the other half of the outage: the daemon
// that writes the audit database is the daemon that is down, so nothing was
// recorded and a later query returned a clean empty result. That reads as no
// violations when it means no checks. The record must be written by the hook.
func TestFailOpenLeavesADurableRecord(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	summary, err := FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary before any record: %v", err)
	}
	if summary.Count != 0 || summary.Earliest != "" || summary.Latest != "" {
		t.Fatalf("clean state reported %d records (%s..%s)",
			summary.Count, summary.Earliest, summary.Latest)
	}
	if summary.Truncated {
		t.Fatal("clean state reported a truncated history")
	}

	for range 3 {
		RecordFailOpen(
			"daemon_unavailable", "claude",
			"PreToolUse", "Bash", "/repo", "no socket",
		)
	}

	summary, err = FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary after recording: %v", err)
	}
	if summary.Count != 3 {
		t.Fatalf("records = %d, want 3", summary.Count)
	}
	if summary.Earliest == "" || summary.Latest == "" {
		t.Fatalf("record window = %q..%q, want both ends set",
			summary.Earliest, summary.Latest)
	}
	if summary.Truncated {
		t.Fatal("three small records should not have hit the size cap")
	}

	path := filepath.Join(stateHome, "agent-gate", "fail-open.jsonl")
	t.Logf("records written to %s", path)
}

// TestRecordFailOpenIgnoresAnEmptyReason keeps the file free of entries for
// calls that were actually evaluated.
func TestRecordFailOpenIgnoresAnEmptyReason(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	RecordFailOpen("", "claude", "PreToolUse", "Bash", "/repo", "")
	summary, err := FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Count != 0 {
		t.Fatalf("records = %d, want 0 for an evaluated call", summary.Count)
	}
}

// TestFailOpenRecordReportsItsOwnTruncation is the regression for a capped
// history presented as a complete one. Past the size cap later calls go
// unrecorded, so a count reported without that qualifier understates the very
// outage the record exists to prove.
func TestFailOpenRecordReportsItsOwnTruncation(t *testing.T) {
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)

	// Fill the record past its cap. Each call carries a long detail so the file
	// reaches the limit without needing a very large number of records.
	detail := strings.Repeat("x", 4096)
	for range 3000 {
		RecordFailOpen(
			"daemon_unavailable", "claude",
			"PreToolUse", "Bash", "/repo", detail,
		)
	}

	summary, err := FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !summary.Truncated {
		t.Fatalf("summary reports %d records as a complete history after the cap",
			summary.Count)
	}
	if summary.Count == 0 {
		t.Fatal("the cap dropped everything, so no outage window survives at all")
	}

	// The file itself must stay under the cap, or the guard bought nothing.
	info, err := os.Stat(filepath.Join(stateHome, "agent-gate", "fail-open.jsonl"))
	if err != nil {
		t.Fatalf("stat record: %v", err)
	}
	if info.Size() > 8<<20 {
		t.Fatalf("record is %d bytes, above its 8 MiB cap", info.Size())
	}
}
