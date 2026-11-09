package auditstorage_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestCanceledSQLiteErrorsPreserveIndependentCleanupFailures(t *testing.T) {
	store, err := openFixtureIntake(t, t.Context(), filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = store.Handle().Close() }()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, canceledError := store.HasPending(canceled)
	if !errors.Is(canceledError, context.Canceled) {
		t.Fatalf("pending-state cancellation = %v", canceledError)
	}

	expired, expire := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer expire()
	_, deadlineError := store.HasPending(expired)
	if !errors.Is(deadlineError, context.DeadlineExceeded) {
		t.Fatalf("pending-state expiration = %v", deadlineError)
	}

	file, err := os.CreateTemp(t.TempDir(), "cleanup")
	if err != nil {
		t.Fatal(err)
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	cleanupError := file.Close()
	if cleanupError == nil {
		t.Fatal("second file closure did not fail")
	}

	joinedCancellation := fmt.Errorf(
		"open and release: %w",
		errors.Join(canceledError, fmt.Errorf("release: %w", canceledError)),
	)
	joinedFailure := fmt.Errorf(
		"open and release: %w",
		errors.Join(canceledError, fmt.Errorf("release: %w", cleanupError)),
	)
	if !auditstorage.CancellationOnly(joinedCancellation) ||
		!auditstorage.MatchesOnly(joinedCancellation, canceled.Err()) {
		t.Error("wrapped cancellation lost its context classification")
	}

	if auditstorage.CancellationOnly(joinedFailure) ||
		auditstorage.MatchesOnly(joinedFailure, canceled.Err()) {
		t.Error("cancellation hid an independent file-close failure")
	}

	nestedFailure := errors.Join(
		joinedCancellation,
		fmt.Errorf("nested cleanup: %w", joinedFailure),
	)
	if auditstorage.CancellationOnly(nestedFailure) ||
		auditstorage.MatchesOnly(nestedFailure, canceled.Err()) {
		t.Error("nested joins hid an independent file-close failure")
	}

	mixedContexts := errors.Join(canceledError, deadlineError)
	if !auditstorage.CancellationOnly(mixedContexts) {
		t.Error("context-free classification rejected cancellation leaves")
	}

	if auditstorage.MatchesOnly(mixedContexts, canceled.Err()) ||
		auditstorage.MatchesOnly(mixedContexts, expired.Err()) {
		t.Error("one matching leaf hid a different context error")
	}

	if auditstorage.MatchesOnly(canceledError, t.Context().Err()) {
		t.Error("active operation accepted an unrelated cancellation")
	}
}

func TestStorageWarningsDistinguishCanceledOpenAndFilesystemFailure(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "warnings.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = logFile.Close() }()
	original := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logFile, nil)))
	defer slog.SetDefault(original)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if database, err := auditstorage.OpenWriter(
		ctx,
		filepath.Join(root, "canceled.db"),
	); !errors.Is(err, context.Canceled) {
		if database != nil {
			_ = database.Close()
		}

		t.Fatalf("open cancellation = %v", err)
	}

	if body, err := os.ReadFile(logPath); err != nil || len(body) != 0 {
		t.Fatalf("canceled storage open logged a warning: %q: %v", body, err)
	}

	blocked := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blocked, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}

	if database, err := auditstorage.OpenWriter(
		ctx,
		filepath.Join(blocked, "audit.db"),
	); err == nil ||
		errors.Is(err, context.Canceled) {
		if database != nil {
			_ = database.Close()
		}

		t.Fatalf("filesystem failure = %v", err)
	}

	body, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(body), "create audit directory") {
		t.Fatalf("filesystem failure did not remain visible: %q: %v", body, err)
	}
}
