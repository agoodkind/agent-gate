package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestLoggerShutdownBoundsBlockedBorrowedWriter(t *testing.T) {
	cfg := testConfig(t)
	database, err := auditstorage.OpenWriter(t.Context(), cfg.AuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	logger, err := audit.NewEventLoggerWithOptions(t.Context(), cfg, nil, audit.LoggerOptions{SharedDB: database})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("codex", "shutdown", "Stop", "info", "hook.allowed", nil)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	closed := make(chan error, 1)
	go func() { closed <- logger.CloseContext(ctx) }()
	select {
	case err := <-closed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("close error = %v", err)
		}
	case <-time.After(time.Second):
		t.Error("shutdown failed to cancel blocked database acquisition")
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.PingContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLoggerShutdownDrainsAfterOwnerCancellation(t *testing.T) {
	cfg := testConfig(t)
	owner, cancelOwner := context.WithCancel(t.Context())
	logger, err := audit.NewEventLoggerWithOptions(owner, cfg, nil, audit.LoggerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	logger.Log("codex", "shutdown", "Stop", "info", "hook.allowed", nil)
	cancelOwner()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := logger.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	events, _, err := audit.QueryReadOnly(t.Context(), cfg, audit.QueryFilter{})
	if err != nil || len(events) != 1 {
		t.Fatalf("accepted events = %v, %v", events, err)
	}
}
