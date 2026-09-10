package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestExportEvaluationsStreamsPastDefaultPage(t *testing.T) {
	setupQueryEnvironment(t)
	base := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for index := range 105 {
		id := fmt.Sprintf("stream-%03d", index)
		appendCLIExportEvaluation(t, id, "event-"+id, "codex", "stream-session", base.Add(time.Duration(index)*time.Second))
	}
	code, stdout, stderr := captureRunExport(t, []string{"evaluations"})
	if code != 0 || stderr != "" {
		t.Fatalf("export = %d, %s", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 105 {
		t.Fatalf("exported %d rows, want 105", len(lines))
	}
	for index, line := range lines {
		if !strings.Contains(line, fmt.Sprintf(`"evaluation_id":"stream-%03d"`, 104-index)) || !strings.Contains(line, `"bucket_id":"`) {
			t.Fatalf("row %d = %s", index, line)
		}
	}
	code, stdout, stderr = captureRunExport(t, []string{"evaluations", "--offset", "99", "--limit", "3"})
	if code != 0 || stderr != "" || strings.Count(stdout, "\n") != 3 || !strings.Contains(stdout, `"evaluation_id":"stream-005"`) || !strings.Contains(stdout, `"evaluation_id":"stream-003"`) {
		t.Fatalf("bounded stream = %d, %s, %s", code, stdout, stderr)
	}
}
