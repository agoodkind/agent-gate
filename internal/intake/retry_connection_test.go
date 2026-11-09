package intake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func retryFixture(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	return string(body)
}

func TestRetryCleanupUsesImmediateWriteAndPreservesConnectionTimeout(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(
			map[bool]string{false: "default", true: "custom"}[custom],
			func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "audit.db")
				database, err := auditstorage.OpenWriter(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = database.Close() }()
				var cfg config.Config
				if err := cfg.PrepareAuditStorage(); err != nil {
					t.Fatal(err)
				}

				store, err := intake.NewStore(t.Context(), database, cfg.AuditStoragePolicy(), nil)
				if err != nil {
					t.Fatal(err)
				}

				expectedTimeout := 5000
				if custom {
					if _, err := database.ExecContext(
						t.Context(),
						retryFixture(t, "retry_custom_timeout.sql"),
					); err != nil {
						t.Fatal(err)
					}

					expectedTimeout = 1234
				}

				receipt, err := store.Append(
					t.Context(),
					intake.Record{
						EventID:        "retry-timeout",
						System:         "codex",
						SessionID:      "retry-timeout",
						EventName:      "Stop",
						RawPayload:     []byte(`{}`),
						NormalizedJSON: []byte(`{}`),
					},
				)
				if err != nil {
					t.Fatal(err)
				}

				if err := store.MarkDeferredPending(
					t.Context(),
					receipt.EventID,
					receipt.ReceiptID,
				); err != nil {
					t.Fatal(err)
				}

				_, claim, err := store.ClaimDeferred(
					t.Context(),
					receipt.ReceiptID,
					"cleanup-owner",
					time.Minute,
				)
				if err != nil {
					t.Fatal(err)
				}

				blocker, err := auditstorage.OpenWriter(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = blocker.Close() }()
				transaction, err := blocker.BeginTx(t.Context(), nil)
				if err != nil {
					t.Fatal(err)
				}

				defer func() { _ = transaction.Rollback() }()
				if _, err := transaction.ExecContext(
					t.Context(),
					retryFixture(t, "retry_hold.sql"),
					receipt.ReceiptID,
				); err != nil {
					t.Fatal(err)
				}

				started := time.Now()
				err = store.ScheduleDeferredRetry(t.Context(), claim, time.Now().Add(time.Second))
				elapsed := time.Since(started)
				var sqliteError sqlite3.Error
				if !errors.As(err, &sqliteError) || sqliteError.Code != sqlite3.ErrBusy {
					t.Fatalf("contended cleanup error = %v", err)
				}

				if elapsed >= time.Second {
					t.Fatalf("immediate retry waited %s", elapsed)
				}

				assertTimeout := func() {
					var timeout int
					if err := database.QueryRowContext(
						t.Context(),
						retryFixture(t, "retry_timeout.sql"),
					).Scan(
						&timeout,
					); err != nil {
						t.Fatal(err)
					}

					if timeout != expectedTimeout {
						t.Fatalf("connection timeout = %d, want %d", timeout, expectedTimeout)
					}
				}
				assertTimeout()
				if err := transaction.Rollback(); err != nil {
					t.Fatal(err)
				}

				now := time.Now()
				if err := store.ScheduleDeferredRetry(
					t.Context(),
					claim,
					now.Add(time.Second),
				); err != nil {
					t.Fatal(err)
				}

				assertTimeout()
				if err := store.ScheduleDeferredRetry(t.Context(), claim, now); !errors.Is(
					err,
					intake.ErrDeferredClaimLost,
				) {
					t.Fatalf("stale cleanup error = %v", err)
				}

				assertTimeout()
				due, err := store.ListDueDeferred(t.Context(), now.Add(2*time.Second), 0, 10)
				if err != nil || len(due) != 1 || due[0] != receipt.ReceiptID {
					t.Fatalf("successful retry due receipts = %v, %v", due, err)
				}

				canceled, cancel := context.WithCancel(t.Context())
				cancel()
				if err := store.ScheduleDeferredRetry(canceled, claim, now); !errors.Is(
					err,
					context.Canceled,
				) {
					t.Fatalf("canceled cleanup error = %v", err)
				}

				assertTimeout()
				t.Logf("saved_timeout_ms=%d busy_attempt_duration=%s", expectedTimeout, elapsed)
			},
		)
	}
}

func TestRetryCleanupPreservesTimeoutWhenSQLiteDeniesTimeoutChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := auditstorage.OpenWriter(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = database.Close() }()
	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}

	store, err := intake.NewStore(t.Context(), database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := store.Append(
		t.Context(),
		intake.Record{
			EventID:        "restore-failure",
			System:         "codex",
			SessionID:      "restore-failure",
			EventName:      "Stop",
			RawPayload:     []byte(`{}`),
			NormalizedJSON: []byte(`{}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.MarkDeferredPending(
		t.Context(),
		receipt.EventID,
		receipt.ReceiptID,
	); err != nil {
		t.Fatal(err)
	}

	_, claim, err := store.ClaimDeferred(
		t.Context(),
		receipt.ReceiptID,
		"restore-owner",
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}

	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	if err := connection.Raw(func(raw any) error {
		sqliteConnection, ok := raw.(*auditstorage.SQLiteConnection)
		if !ok {
			return errors.New("retry connection did not use SQLite")
		}

		sqliteConnection.RegisterAuthorizer(func(action int, name, value, _ string) int {
			if action == sqlite3.SQLITE_PRAGMA && name == "busy_timeout" && value != "" {
				return sqlite3.SQLITE_DENY
			}

			return sqlite3.SQLITE_OK
		})

		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = database.ExecContext(t.Context(), retryFixture(t, "retry_custom_timeout.sql"))
	var sqliteError sqlite3.Error
	if !errors.As(err, &sqliteError) || sqliteError.Code != sqlite3.ErrAuth {
		t.Fatalf("SQLite allowed a timeout change: %v", err)
	}

	now := time.Now()
	if err := store.ScheduleDeferredRetry(t.Context(), claim, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	var timeout int
	if err := database.QueryRowContext(t.Context(), retryFixture(t, "retry_timeout.sql")).Scan(
		&timeout,
	); err != nil {
		t.Fatal(err)
	}

	if timeout != 5000 {
		t.Fatalf("connection timeout = %d", timeout)
	}

	due, err := store.ListDueDeferred(t.Context(), now.Add(2*time.Second), 0, 10)
	if err != nil || len(due) != 1 || due[0] != receipt.ReceiptID {
		t.Fatalf("retry did not schedule the receipt: %v, %v", due, err)
	}
}
