package audit_test

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
)

func boolp(v bool) *bool { return &v }

func testConfig(t *testing.T, sqlite bool) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Audit: config.Audit{
			Enabled: boolp(true),
			Level:   "debug",
			Outputs: config.AuditOutput{
				JSONL: config.AuditJSONLOutput{
					Enabled:          boolp(true),
					EventsDir:        filepath.Join(dir, "events"),
					PayloadsDir:      filepath.Join(dir, "payloads"),
					WriteRawPayloads: boolp(true),
				},
				SQLite: config.AuditSQLiteOutput{
					Enabled: boolp(sqlite),
					Path:    filepath.Join(dir, "sqlite", "audit.db"),
				},
			},
		},
	}
}

func TestEventLogger_WritesJSONLAndPayloadSidecar(t *testing.T) {
	cfg := testConfig(t, false)
	logger, err := audit.NewEventLogger(cfg, nil)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}

	logger.Log("claude", "session-1", "PreToolUse", "debug", "hook.raw_payload", map[string]any{
		"system":      "claude",
		"session_id":  "session-1",
		"event":       "PreToolUse",
		"raw_payload": `{"hello":"world"}`,
	})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, cfg.AuditEventsDir())
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].RawPayloadHash == "" {
		t.Fatalf("raw payload hash missing: %#v", events[0])
	}
	payloadPath := payloadPath(cfg.AuditPayloadsDir(), events[0].RawPayloadHash)
	data, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	if string(data) != `{"hello":"world"}` {
		t.Fatalf("payload sidecar = %q", string(data))
	}
}

func TestEventLogger_WritesSQLiteAndDedups(t *testing.T) {
	cfg := testConfig(t, true)
	logger, err := audit.NewEventLogger(cfg, nil)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}

	attrs := map[string]any{
		"system":         "claude",
		"session_id":     "session-1",
		"event":          "PreToolUse",
		"tool_name":      "Bash",
		"tool_use_id":    "toolu_1",
		"decision":       "block",
		"blocking_rules": []any{"use-make-not-go-direct"},
		"ti_command":     "go build ./...",
	}
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", attrs)
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", attrs)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events := readEvents(t, cfg.AuditEventsDir())
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].Decision.RulesMatched; len(got) != 1 || got[0] != "use-make-not-go-direct" {
		t.Fatalf("rules matched = %#v", got)
	}

	db, err := sql.Open("sqlite3", cfg.AuditSQLitePath())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow("select count(*) from events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("sqlite events = %d, want 1", count)
	}
	if err := db.QueryRow("select count(*) from violations where rule = 'use-make-not-go-direct'").Scan(&count); err != nil {
		t.Fatalf("count violations: %v", err)
	}
	if count != 1 {
		t.Fatalf("sqlite violations = %d, want 1", count)
	}
}

func TestQuery_JSONLFallback(t *testing.T) {
	cfg := testConfig(t, false)
	logger, err := audit.NewEventLogger(cfg, nil)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", map[string]any{
		"system":         "claude",
		"session_id":     "session-1",
		"event":          "PreToolUse",
		"tool_name":      "Bash",
		"decision":       "block",
		"blocking_rules": []any{"use-make-not-go-direct"},
	})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events, source, err := audit.Query(cfg, audit.QueryFilter{Decision: "block", Rule: "use-make-not-go-direct"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if source != "jsonl" {
		t.Fatalf("source = %q, want jsonl", source)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

func readEvents(t *testing.T, dir string) []audit.Event {
	t.Helper()
	var path string
	if err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Base(p) == "events.jsonl" {
			path = p
		}
		return nil
	}); err != nil {
		t.Fatalf("walk events: %v", err)
	}
	if path == "" {
		t.Fatal("events.jsonl not found")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	defer f.Close()

	var out []audit.Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event audit.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan events: %v", err)
	}
	return out
}

func payloadPath(root, hash string) string {
	hash = hash[len("sha256:"):]
	return filepath.Join(root, "sha256", hash[:2], hash[2:4], hash+".json")
}
