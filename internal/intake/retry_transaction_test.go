package intake_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

type retryDatabase struct {
	database *sql.DB
	store    *intake.Store
	claim    intake.DeferredClaim
	path     string
}

func openRetryDatabase(t *testing.T) retryDatabase {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := auditstorage.OpenWriter(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	var cfg config.Config
	if err := cfg.PrepareAuditStorage(); err != nil {
		t.Fatal(err)
	}
	store, err := intake.NewStore(t.Context(), database, cfg.AuditStoragePolicy(), nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{EventID: t.Name(), RawPayload: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeferredPending(t.Context(), receipt.EventID, receipt.ReceiptID); err != nil {
		t.Fatal(err)
	}
	_, claim, err := store.ClaimDeferred(t.Context(), receipt.ReceiptID, "retry-owner", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return retryDatabase{database: database, store: store, claim: claim, path: path}
}

func configureRetryConnection(t *testing.T, database *sql.DB, configure func(*auditstorage.SQLiteConnection) error) {
	t.Helper()
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = connection.Raw(func(raw any) error { return configure(raw.(*auditstorage.SQLiteConnection)) })
	closeError := connection.Close()
	if err != nil || closeError != nil {
		t.Fatalf("configure SQLite connection: %v, %v", err, closeError)
	}
}

func TestRetryCleanupDiscardsConnectionAfterFailedRollback(t *testing.T) {
	fixture := openRetryDatabase(t)
	if _, err := fixture.database.ExecContext(t.Context(), retryFixture(t, "retry_abort.sql")); err != nil {
		t.Fatal(err)
	}
	var original *auditstorage.SQLiteConnection
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		original = connection
		connection.RegisterAuthorizer(func(action int, name, _, _ string) int {
			if action == sqlite3.SQLITE_TRANSACTION && name == "ROLLBACK" {
				return sqlite3.SQLITE_DENY
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	err := fixture.store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now())
	if err == nil || !strings.Contains(err.Error(), "retry update failed") || !strings.Contains(err.Error(), "rollback deferred retry: not authorized") {
		t.Fatalf("update and rollback errors = %v", err)
	}
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		if connection == original || !connection.AutoCommit() {
			t.Fatal("failed rollback returned the active connection to its pool")
		}
		return nil
	})
	if _, err := fixture.database.ExecContext(t.Context(), retryFixture(t, "retry_drop_trigger.sql")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now()); err != nil {
		t.Fatal(err)
	}
	if fixture.database.Stats().OpenConnections != 1 {
		t.Fatal("retry changed the writer connection count")
	}
}

func TestRetryCleanupPreservesTimeoutAfterInterruptedWrite(t *testing.T) {
	fixture := openRetryDatabase(t)
	if _, err := fixture.database.ExecContext(t.Context(), retryFixture(t, "retry_custom_timeout.sql")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var original *auditstorage.SQLiteConnection
	var rollbacks atomic.Int32
	var explicitRollbacks atomic.Int32
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		original = connection
		connection.RegisterRollbackHook(func() { rollbacks.Add(1) })
		connection.RegisterAuthorizer(func(action int, name, _, _ string) int {
			if action == sqlite3.SQLITE_TRANSACTION && name == "ROLLBACK" {
				explicitRollbacks.Add(1)
			}
			return sqlite3.SQLITE_OK
		})
		return connection.RegisterFunc("retry_cancel", func() int { cancel(); return 0 }, false)
	})
	if _, err := fixture.database.ExecContext(t.Context(), retryFixture(t, "retry_interrupt.sql")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := fixture.store.ScheduleDeferredRetry(ctx, fixture.claim, time.Now()); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted retry error = %v", err)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("interrupted retry did not stop promptly")
	}
	if rollbacks.Load() != 1 {
		t.Fatalf("interrupted write rollbacks = %d", rollbacks.Load())
	}
	if explicitRollbacks.Load() != 0 {
		t.Fatal("cleanup issued ROLLBACK after SQLite already rolled back the interrupted write")
	}
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		if connection != original || !connection.AutoCommit() {
			t.Fatal("interrupted write did not preserve the cleaned connection")
		}
		return nil
	})
	var timeout int
	if err := fixture.database.QueryRowContext(t.Context(), retryFixture(t, "retry_timeout.sql")).Scan(&timeout); err != nil {
		t.Fatal(err)
	}
	if timeout != 1234 {
		t.Fatalf("timeout after interruption = %d", timeout)
	}
	if _, err := fixture.database.ExecContext(t.Context(), retryFixture(t, "retry_drop_trigger.sql")); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now()); err != nil {
		t.Fatalf("interrupted write changed the durable claim: %v", err)
	}
}

func TestRetryCleanupRejectsStaleWALSnapshotPromptly(t *testing.T) {
	fixture := openRetryDatabase(t)
	writer, err := auditstorage.OpenWriter(t.Context(), fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	var commitError error
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		connection.RegisterAuthorizer(func(action int, name, _, _ string) int {
			if action == sqlite3.SQLITE_UPDATE && name == "intake_deferred" {
				_, commitError = writer.ExecContext(t.Context(), retryFixture(t, "retry_hold.sql"), fixture.claim.ReceiptID)
			}
			return sqlite3.SQLITE_OK
		})
		return nil
	})
	started := time.Now()
	err = fixture.store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now())
	var sqliteError sqlite3.Error
	if !errors.As(err, &sqliteError) || sqliteError.ExtendedCode != sqlite3.ErrBusySnapshot {
		t.Fatalf("snapshot upgrade = %v", err)
	}
	if commitError != nil {
		t.Fatalf("competing writer failed: %v", commitError)
	}
	if time.Since(started) >= time.Second {
		t.Fatal("stale snapshot waited for the busy timeout")
	}
	configureRetryConnection(t, fixture.database, func(connection *auditstorage.SQLiteConnection) error {
		connection.RegisterAuthorizer(nil)
		return nil
	})
	if err := fixture.store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestRetryCleanupRejectsUnsupportedPoolsBeforeBeginning(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(map[bool]string{false: "expanded", true: "foreign"}[foreign], func(t *testing.T) {
			fixture := openRetryDatabase(t)
			database := fixture.database
			var begins atomic.Int32
			authorize := func(action int, name, _, _ string) int {
				if action == sqlite3.SQLITE_TRANSACTION && name == "BEGIN" {
					begins.Add(1)
				}
				return sqlite3.SQLITE_OK
			}
			if foreign {
				var err error
				database, err = sql.Open("sqlite3", fixture.path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = database.Close() }()
				database.SetMaxOpenConns(1)
				connection, err := database.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if err := connection.Raw(func(raw any) error {
					raw.(*sqlite3.SQLiteConn).RegisterAuthorizer(authorize)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				if err := connection.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				configureRetryConnection(t, database, func(connection *auditstorage.SQLiteConnection) error {
					connection.RegisterAuthorizer(authorize)
					return nil
				})
				database.SetMaxOpenConns(2)
			}
			var cfg config.Config
			if err := cfg.PrepareAuditStorage(); err != nil {
				t.Fatal(err)
			}
			store, err := intake.NewStore(t.Context(), database, cfg.AuditStoragePolicy(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ScheduleDeferredRetry(t.Context(), fixture.claim, time.Now()); err == nil {
				t.Fatal("retry accepted an unsupported writer pool")
			}
			if begins.Load() != 0 {
				t.Fatal("retry started a transaction on an unsupported writer pool")
			}
		})
	}
}
