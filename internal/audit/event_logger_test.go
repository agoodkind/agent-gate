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
	logger, err := audit.NewEventLoggerWithOptions(context.Background(), cfg, nil, audit.LoggerOptions{QueueLimit: 0})
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

func TestEventLogger_DurableReplayDoesNotDuplicateViolations(t *testing.T) {
	cfg := testConfig(t)
	attrs := audit.Attrs{
		"system":         audit.NewStringValue("codex"),
		"session_id":     audit.NewStringValue("session-1"),
		"event":          audit.NewStringValue("PreToolUse"),
		"decision":       audit.NewStringValue("block"),
		"blocking_rules": audit.NewStringSliceValue([]string{"durable-rule"}),
	}

	for range 2 {
		logger, err := audit.NewEventLoggerWithOptions(
			context.Background(), cfg, nil, audit.LoggerOptions{QueueLimit: 0},
		)
		if err != nil {
			t.Fatalf("NewEventLogger: %v", err)
		}
		if err := logger.LogDurable(
			context.Background(), "codex", "session-1", "PreToolUse", "info",
			"hook.blocked", attrs,
		); err != nil {
			t.Fatalf("LogDurable: %v", err)
		}
		if err := logger.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}

	database, err := sql.Open("sqlite3", cfg.AuditSQLitePath())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRow(
		"select count(*) from violations where rule = 'durable-rule'",
	).Scan(&count); err != nil {
		t.Fatalf("count violations: %v", err)
	}
	if count != 1 {
		t.Fatalf("sqlite violations = %d, want 1 after durable replay", count)
	}
}

func TestEventLogger_NormalizedReplayPreservesIdentityAndDoesNotDuplicate(t *testing.T) {
	cfg := testConfig(t)
	attrs := audit.Attrs{
		"system":         audit.NewStringValue("codex"),
		"session_id":     audit.NewStringValue("session-normalized"),
		"event":          audit.NewStringValue("PreToolUse"),
		"decision":       audit.NewStringValue("block"),
		"blocking_rules": audit.NewStringSliceValue([]string{"normalized-rule"}),
	}
	logger, err := audit.NewEventLoggerWithOptions(
		context.Background(), cfg, nil, audit.LoggerOptions{QueueLimit: 0},
	)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	entry := logger.Normalize(
		"codex", "session-normalized", "PreToolUse", "info", "hook.blocked", attrs,
	)
	if entry.Event.EventID == "" || entry.Event.Time == "" {
		t.Fatalf("normalized entry identity = %+v", entry.Event)
	}
	if err := logger.LogNormalizedDurable(context.Background(), entry); err != nil {
		t.Fatalf("LogNormalizedDurable: %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	replayLogger, err := audit.NewEventLoggerWithOptions(
		context.Background(), cfg, nil, audit.LoggerOptions{QueueLimit: 0},
	)
	if err != nil {
		t.Fatalf("NewEventLogger replay: %v", err)
	}
	if err := replayLogger.LogNormalizedDurable(context.Background(), entry); err != nil {
		t.Fatalf("LogNormalizedDurable replay: %v", err)
	}
	if err := replayLogger.Close(); err != nil {
		t.Fatalf("Close replay: %v", err)
	}

	database, err := sql.Open("sqlite3", cfg.AuditSQLitePath())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	var storedTime string
	if err := database.QueryRow(
		"select count(*), min(time) from events where event_id = ?", entry.Event.EventID,
	).Scan(&count, &storedTime); err != nil {
		t.Fatalf("query normalized event: %v", err)
	}
	if count != 1 || storedTime != entry.Event.Time {
		t.Fatalf("normalized event count/time = %d/%q, want 1/%q", count, storedTime, entry.Event.Time)
	}
}

func TestQuery_SQLite(t *testing.T) {
	cfg := testConfig(t)
	logger, err := audit.NewEventLoggerWithOptions(context.Background(), cfg, nil, audit.LoggerOptions{QueueLimit: 0})
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
