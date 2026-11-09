package audit_test

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

func TestCanceledAcceptedAuditBatchKeepsFailureDiagnostic(t *testing.T) {
	root := t.TempDir()
	database, err := auditstorage.OpenWriter(t.Context(), filepath.Join(root, "audit.db"))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()
	logPath := filepath.Join(root, "warnings.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logFile.Close() }()
	log := slog.New(slog.NewTextHandler(logFile, nil))
	enabled := true
	cfg := &config.Config{Audit: config.Audit{Enabled: &enabled}}
	logger, err := audit.NewEventLoggerWithOptions(
		t.Context(),
		cfg,
		log,
		audit.LoggerOptions{SharedDB: database},
	)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logger.Close() }()
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = transaction.Rollback() }()
	logger.Log("codex", "accepted", "PreToolUse", "info", "accepted event", nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := logger.CloseContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled logger close = %v", err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(body), "audit batch write failed") {
		t.Fatalf("discarded accepted batch lost its warning: %q: %v", body, err)
	}

	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
}
