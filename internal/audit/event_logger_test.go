package audit_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
)

func boolp(v bool) *bool { return &v }

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Audit: config.Audit{
			Enabled: boolp(true),
			Level:   "debug",
			Outputs: config.AuditOutput{
				SQLite: config.AuditSQLiteOutput{
					Path: filepath.Join(dir, "sqlite", "audit.db"),
				},
			},
		},
	}
}

func TestEventLogger_WritesSQLiteAndDedups(t *testing.T) {
	cfg := testConfig(t)
	logger, err := audit.NewEventLoggerContext(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}

	attrs := audit.Attrs{
		"system":         audit.NewStringValue("claude"),
		"session_id":     audit.NewStringValue("session-1"),
		"event":          audit.NewStringValue("PreToolUse"),
		"tool_name":      audit.NewStringValue("Bash"),
		"tool_use_id":    audit.NewStringValue("toolu_1"),
		"decision":       audit.NewStringValue("block"),
		"blocking_rules": audit.NewStringSliceValue([]string{"use-make-not-go-direct"}),
		"ti_command":     audit.NewStringValue("go build ./..."),
	}
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", attrs)
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", attrs)
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err := sql.Open("sqlite3", cfg.AuditSQLitePath())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow("select count(*) from events").Scan(&count); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if count != 1 {
		t.Fatalf("sqlite events = %d, want 1 (dedup)", count)
	}
	if err := db.QueryRow("select count(*) from violations where rule = 'use-make-not-go-direct'").Scan(&count); err != nil {
		t.Fatalf("count violations: %v", err)
	}
	if count != 1 {
		t.Fatalf("sqlite violations = %d, want 1", count)
	}
}

func TestQuery_SQLite(t *testing.T) {
	cfg := testConfig(t)
	logger, err := audit.NewEventLoggerContext(context.Background(), cfg, nil)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	logger.Log("claude", "session-1", "PreToolUse", "info", "hook.blocked", audit.Attrs{
		"system":         audit.NewStringValue("claude"),
		"session_id":     audit.NewStringValue("session-1"),
		"event":          audit.NewStringValue("PreToolUse"),
		"tool_name":      audit.NewStringValue("Bash"),
		"decision":       audit.NewStringValue("block"),
		"blocking_rules": audit.NewStringSliceValue([]string{"use-make-not-go-direct"}),
	})
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	events, source, err := audit.Query(cfg, audit.QueryFilter{Decision: "block", Rule: "use-make-not-go-direct"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if source != "sqlite" {
		t.Fatalf("source = %q, want sqlite", source)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if got := events[0].Decision.RulesMatched; len(got) != 1 || got[0] != "use-make-not-go-direct" {
		t.Fatalf("rules matched = %#v", got)
	}
}
