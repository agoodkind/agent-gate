package hook

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// failOpenRecordName is the file the hook appends to when it lets a call
// through unevaluated. It sits beside the audit database but is written by the
// hook process, never by the daemon, which is the whole point: the daemon that
// would normally record the event is the one that is not answering.
const failOpenRecordName = "fail-open.jsonl"

// maxFailOpenRecordBytes caps the file so a daemon that stays down for days
// cannot fill the disk. Past the cap the hook stops appending; the earliest
// records are the ones that matter, because they date the start of the outage.
const maxFailOpenRecordBytes = 8 << 20

// failOpenNow is the clock the record stamps with. It is a package var so a
// test can pin the timestamp, matching auditNow in internal/daemon.
var failOpenNow = time.Now

// FailOpenRecord is one call that agent-gate allowed without evaluating.
type FailOpenRecord struct {
	At        string `json:"at"`
	Reason    string `json:"reason"`
	System    string `json:"system"`
	EventName string `json:"event_name"`
	ToolName  string `json:"tool_name,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

// FailOpenRecordPath returns the file the hook appends unevaluated calls to,
// so an operator can read the raw records after a summary points at them.
func FailOpenRecordPath() string {
	return filepath.Join(config.DefaultStateDir(), failOpenRecordName)
}

// RecordFailOpen appends one unevaluated call to a file the daemon does not
// own, so an outage leaves evidence that outlives it.
//
// Without this, a daemon that will not start produces no audit rows at all, and
// a later query for blocks in that window returns a clean empty result. That
// reads as "nothing was violated" when it means "nothing was checked". Every
// error here is swallowed on purpose: failing to write a record must never
// break the tool call the hook already decided to allow.
func RecordFailOpen(reason FailOpenReason, system System, eventName string, toolName string, cwd string, detail string) {
	if reason == "" {
		return
	}
	path := FailOpenRecordPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if info, err := os.Stat(path); err == nil && info.Size() > maxFailOpenRecordBytes {
		return
	}
	record := FailOpenRecord{
		At:        failOpenNow().UTC().Format(time.RFC3339Nano),
		Reason:    string(reason),
		System:    system.String(),
		EventName: eventName,
		ToolName:  toolName,
		CWD:       cwd,
		Detail:    detail,
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		return
	}
	// #nosec G304 -- the path is derived from the XDG state directory, not input.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = file.Write(append(encoded, '\n'))
}

// FailOpenRecordSummary reports how many calls went unevaluated and when, for
// an operator asking whether enforcement was ever absent. It returns zero and
// empty strings when the file does not exist, which is the healthy case.
func FailOpenRecordSummary() (int, string, string, error) {
	path := FailOpenRecordPath()
	// #nosec G304 -- the path is derived from the XDG state directory, not input.
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, "", "", nil
		}
		slog.Warn("read fail-open record failed", "path", path, "err", err)
		return 0, "", "", fmt.Errorf("read fail-open record: %w", err)
	}
	count := 0
	earliest := ""
	latest := ""
	for _, line := range splitLines(data) {
		var record FailOpenRecord
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		count++
		if earliest == "" || record.At < earliest {
			earliest = record.At
		}
		if record.At > latest {
			latest = record.At
		}
	}
	return count, earliest, latest, nil
}

// splitLines returns the non-empty lines of data without allocating a string
// for the whole file.
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
