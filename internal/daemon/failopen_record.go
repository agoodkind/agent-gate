package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/logstorage"
)

// Hooks write outage evidence independently of daemon-owned audit databases.
const (
	failOpenRecordName     = logstorage.FailOpenName
	failOpenTruncatedName  = logstorage.FailOpenTruncatedName
	maxFailOpenRecordBytes = logstorage.FailOpenMaxBytes
)

// Tests replace failOpenNow to compare records against a fixed time.
var failOpenNow = time.Now

// FailOpenRecord records one unevaluated call.
type FailOpenRecord struct {
	At        string `json:"at"`
	Reason    string `json:"reason"`
	System    string `json:"system"`
	EventName string `json:"event_name"`
	ToolName  string `json:"tool_name,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// FailOpenRecordPath returns the path for evidence of unevaluated calls.
func FailOpenRecordPath() string {
	return filepath.Join(config.DefaultStateDir(), failOpenRecordName)
}

// RecordFailOpen records an unevaluated call even when the daemon cannot answer.
// A write error does not change the hook response.
func RecordFailOpen(reason string, system string, eventName string, toolName string, cwd string, detail string) {
	if reason == "" {
		return
	}
	record := FailOpenRecord{
		At:        failOpenNow().UTC().Format(time.RFC3339Nano),
		Reason:    reason,
		System:    system,
		EventName: eventName,
		ToolName:  toolName,
		CWD:       cwd,
		Detail:    detail,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		slog.Warn("encode fail-open record failed", "err", err)
		return
	}
	if err := logstorage.AppendFailOpen(config.DefaultStateDir(), append(encoded, '\n')); err != nil {
		slog.Warn("write fail-open record failed", "err", err)
	}
}

// FailOpenSummary counts recorded unevaluated calls within each time window.
type FailOpenSummary struct {
	// Count includes only retained records.
	Count       int
	LastHour    int
	Last24Hours int
	Last7Days   int
	Last90Days  int
	// Earliest and Latest identify the first and last retained timestamps.
	Earliest string
	Latest   string
	// Truncated reports that the record omits some unevaluated calls.
	Truncated bool
}

// FailOpenRecordSummary counts retained evidence and reports any truncation.
func FailOpenRecordSummary() (FailOpenSummary, error) {
	var summary FailOpenSummary
	now := failOpenNow()
	path := FailOpenRecordPath()
	data, readErr := logstorage.ReadFailOpen(config.DefaultStateDir())
	marker := filepath.Join(config.DefaultStateDir(), failOpenTruncatedName)
	if _, err := os.Lstat(marker); err == nil {
		summary.Truncated = true
	} else if !os.IsNotExist(err) {
		return summary, fmt.Errorf("read fail-open truncation marker: %w", err)
	}
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return summary, nil
		}
		slog.Warn("read fail-open record failed", "path", path, "err", readErr)
		return summary, fmt.Errorf("read fail-open record: %w", readErr)
	}
	for _, line := range splitLines(data) {
		var record FailOpenRecord
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		recordedAt, err := time.Parse(time.RFC3339Nano, record.At)
		if err != nil {
			return summary, fmt.Errorf("parse fail-open record timestamp: %w", err)
		}
		age := now.Sub(recordedAt)
		if age >= 0 {
			if age <= time.Hour {
				summary.LastHour++
			}
			if age <= 24*time.Hour {
				summary.Last24Hours++
			}
			if age <= 7*24*time.Hour {
				summary.Last7Days++
			}
			if age <= 90*24*time.Hour {
				summary.Last90Days++
			}
		}
		summary.Count++
		if summary.Earliest == "" || record.At < summary.Earliest {
			summary.Earliest = record.At
		}
		if record.At > summary.Latest {
			summary.Latest = record.At
		}
	}
	return summary, nil
}

func splitLines(data []byte) [][]byte {
	lines := make([][]byte, 0)
	start := 0
	for index, character := range data {
		if character != '\n' {
			continue
		}
		if index > start {
			lines = append(lines, data[start:index])
		}
		start = index + 1
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
