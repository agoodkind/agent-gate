package audit_test

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func normalizerSQL(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func dedupAttempts(t *testing.T, database *sql.DB) int {
	t.Helper()
	var count int
	if err := database.QueryRowContext(
		t.Context(),
		normalizerSQL(t, "count_dedup_attempts.sql"),
	).Scan(
		&count,
	); err != nil {
		t.Fatal(err)
	}

	return count
}

func TestNormalizerPreservesFilteringHashAndStoredIdentity(t *testing.T) {
	cfg := testConfig(t)
	cfg.Audit.Level = "warn"
	database, err := auditstorage.OpenWriter(t.Context(), cfg.AuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()
	logger, err := audit.NewEventLoggerWithOptions(
		t.Context(),
		cfg,
		nil,
		audit.LoggerOptions{SharedDB: database},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logger.Close() }()
	normalizer := audit.NewNormalizer(cfg)
	attrs := audit.Attrs{
		"system":         audit.NewStringValue("codex"),
		"raw_payload":    audit.NewStringValue("original input"),
		"ti_command":     audit.NewStringValue("echo audit"),
		"blocking_rules": audit.NewStringSliceValue([]string{"audit-rule"}),
	}
	filtered := normalizer.Normalize(
		"claude",
		"normalization",
		"Stop",
		"info",
		"hook.blocked",
		attrs,
	)
	if filtered.Event.EventID != "" {
		t.Fatal("normalizer ignored the configured minimum level")
	}

	entry := normalizer.Normalize("claude", "normalization", "Stop", "warn", "hook.blocked", attrs)
	logged := logger.Normalize("claude", "normalization", "Stop", "warn", "hook.blocked", attrs)
	expectedHash := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("original input")))
	if entry.Event.RawPayloadHash != expectedHash || entry.Event.System != "codex" ||
		entry.Event.Operation.Command != "echo audit" ||
		entry.Event.EventID != logged.Event.EventID ||
		entry.Fingerprint != logged.Fingerprint ||
		entry.Event.EventID == "" ||
		entry.Event.Time == "" {
		t.Fatalf("normalized entry = %+v", entry)
	}

	if err := logger.LogNormalizedDurable(t.Context(), entry); err != nil {
		t.Fatal(err)
	}

	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	var identifier, storedTime, storedHash string
	if err := database.QueryRowContext(
		t.Context(),
		normalizerSQL(t, "normalization_entry.sql"),
	).Scan(
		&identifier,
		&storedTime,
		&storedHash,
	); err != nil {
		t.Fatal(err)
	}

	if identifier != entry.Event.EventID || storedTime != entry.Event.Time ||
		storedHash != expectedHash {
		t.Fatalf("stored identity = %q %q %q", identifier, storedTime, storedHash)
	}

	cfg.Audit.Enabled = boolp(false)
	if disabled := audit.NewNormalizer(cfg).Normalize(
		"codex",
		"disabled",
		"Stop",
		"warn",
		"hook.blocked",
		attrs,
	); disabled.Event.EventID != "" {
		t.Fatal("disabled audit normalized an entry")
	}

	defaults := audit.NewNormalizer(nil)
	if debug := defaults.Normalize(
		"codex",
		"default",
		"Stop",
		"debug",
		"hook.allowed",
		nil,
	); debug.Event.EventID != "" {
		t.Fatal("default level included a debug event")
	}

	if info := defaults.Normalize(
		"codex",
		"default",
		"Stop",
		"info",
		"hook.allowed",
		nil,
	); info.Event.EventID == "" {
		t.Fatal("default level omitted an info event")
	}
}

func TestLoggerExpiresRecentDuplicateBeforeEvictingLiveEntry(t *testing.T) {
	cfg := testConfig(t)
	cfg.Performance.Limits.AuditDedupCacheSize = 2
	cfg.Performance.Intervals.AuditDedupTTLMs = 2000
	database, err := auditstorage.OpenWriter(t.Context(), cfg.AuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()
	if _, err := database.ExecContext(
		t.Context(),
		normalizerSQL(t, "dedup_attempts.sql"),
	); err != nil {
		t.Fatal(err)
	}

	logger, err := audit.NewEventLoggerWithOptions(
		t.Context(),
		cfg,
		nil,
		audit.LoggerOptions{SharedDB: database},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logger.Close() }()
	write := func(session string) {
		t.Helper()
		if err := logger.LogDurable(
			t.Context(),
			"codex",
			session,
			"Stop",
			"info",
			"hook.allowed",
			nil,
		); err != nil {
			t.Fatal(err)
		}
	}
	write("A")
	time.Sleep(time.Second)
	write("B")
	time.Sleep(500 * time.Millisecond)
	write("A")
	if attempts := dedupAttempts(t, database); attempts != 2 {
		t.Fatalf("duplicate A changed its write count: %d", attempts)
	}

	time.Sleep(700 * time.Millisecond)
	write("C")
	write("B")
	if attempts := dedupAttempts(t, database); attempts != 3 {
		t.Fatalf("expired A evicted live B: %d attempts", attempts)
	}
}

func TestLoggerLazyDeduplicationExpiresEvictsAndRetriesFailedWrites(t *testing.T) {
	for _, scenario := range []string{"expiry", "capacity", "retry"} {
		t.Run(
			scenario,
			func(t *testing.T) {
				cfg := testConfig(t)
				cfg.Performance.Limits.AuditDedupCacheSize = 2
				cfg.Performance.Intervals.AuditDedupTTLMs = 100
				if scenario != "expiry" {
					cfg.Performance.Intervals.AuditDedupTTLMs = 3600000
				}

				database, err := auditstorage.OpenWriter(t.Context(), cfg.AuditSQLitePath())
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = database.Close() }()
				if _, err := database.ExecContext(
					t.Context(),
					normalizerSQL(t, "dedup_attempts.sql"),
				); err != nil {
					t.Fatal(err)
				}

				logger, err := audit.NewEventLoggerWithOptions(
					t.Context(),
					cfg,
					nil,
					audit.LoggerOptions{SharedDB: database},
				)
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = logger.Close() }()
				entry := logger.Normalize("codex", "dedup", "Stop", "info", "hook.allowed", nil)
				if scenario == "retry" {
					if _, err := database.ExecContext(
						t.Context(),
						normalizerSQL(t, "block_dedup_write.sql"),
					); err != nil {
						t.Fatal(err)
					}

					if err := logger.LogNormalizedDurable(t.Context(), entry); err == nil {
						t.Fatal("blocked audit write succeeded")
					}

					if _, err := database.ExecContext(
						t.Context(),
						normalizerSQL(t, "unblock_dedup_write.sql"),
					); err != nil {
						t.Fatal(err)
					}
				}

				for range 2 {
					if err := logger.LogNormalizedDurable(t.Context(), entry); err != nil {
						t.Fatal(err)
					}
				}

				if count := dedupAttempts(t, database); count != 1 {
					t.Fatalf("duplicate write attempts = %d", count)
				}

				switch scenario {
				case "expiry":
					time.Sleep(150 * time.Millisecond)
					if err := logger.LogNormalizedDurable(t.Context(), entry); err != nil {
						t.Fatal(err)
					}

					if count := dedupAttempts(t, database); count != 2 {
						t.Fatalf("expired write attempts = %d", count)
					}
				case "capacity":
					for _, session := range []string{"second", "third"} {
						if err := logger.LogDurable(
							t.Context(),
							"codex",
							session,
							"Stop",
							"info",
							"hook.allowed",
							nil,
						); err != nil {
							t.Fatal(err)
						}
					}

					if err := logger.LogNormalizedDurable(t.Context(), entry); err != nil {
						t.Fatal(err)
					}

					if count := dedupAttempts(t, database); count != 4 {
						t.Fatalf("evicted write attempts = %d", count)
					}
				}
			},
		)
	}
}
