package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestMaintenanceLeaseCompareAndSetAndOwnedRelease(t *testing.T) {
	database := openLeaseTestDatabase(t)
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	first := maintenanceLease{owner: "one", runID: "run-one", ttl: time.Minute}
	firstTransaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin first lease transaction: %v", err)
	}
	if err := first.acquireTransaction(t.Context(), firstTransaction, now); err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if err := firstTransaction.Commit(); err != nil {
		t.Fatalf("commit first lease: %v", err)
	}
	second := maintenanceLease{owner: "two", runID: "run-two", ttl: time.Minute}
	secondTransaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin second lease transaction: %v", err)
	}
	if err := second.acquireTransaction(t.Context(), secondTransaction, now); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("acquire second lease = %v, want maintenance busy", err)
	}
	_ = secondTransaction.Rollback()
	if err := second.release(t.Context(), database); err != nil {
		t.Fatalf("release unowned lease: %v", err)
	}
	if err := first.renew(t.Context(), database, now.Add(time.Second)); err != nil {
		t.Fatalf("renew owned lease: %v", err)
	}
	if err := first.release(t.Context(), database); err != nil {
		t.Fatalf("release owned lease: %v", err)
	}
}

func TestMaintenanceLeaseTakesOverExpiredLease(t *testing.T) {
	database := openLeaseTestDatabase(t)
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	first := maintenanceLease{owner: "one", runID: "run-one", ttl: time.Minute}
	firstTransaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin first lease transaction: %v", err)
	}
	if err := first.acquireTransaction(t.Context(), firstTransaction, now); err != nil {
		t.Fatalf("acquire first lease: %v", err)
	}
	if err := firstTransaction.Commit(); err != nil {
		t.Fatalf("commit first lease: %v", err)
	}
	second := maintenanceLease{owner: "two", runID: "run-two", ttl: time.Minute}
	secondTransaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin second lease transaction: %v", err)
	}
	if err := second.acquireTransaction(t.Context(), secondTransaction, now.Add(2*time.Minute)); err != nil {
		t.Fatalf("acquire expired lease: %v", err)
	}
	if err := secondTransaction.Commit(); err != nil {
		t.Fatalf("commit expired lease takeover: %v", err)
	}
	if err := first.renew(t.Context(), database, now.Add(2*time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("renew replaced lease = %v, want lease lost", err)
	}
}

func TestReleaseLeaseBoundedIgnoresApplyCancellation(t *testing.T) {
	database := openLeaseTestDatabase(t)
	lease := maintenanceLease{owner: "one", runID: "run-one", ttl: time.Minute}
	transaction, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin lease transaction: %v", err)
	}
	if err := lease.acquireTransaction(t.Context(), transaction, time.Now().UTC()); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit lease: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := releaseLeaseBounded(ctx, database, lease); err != nil {
		t.Fatalf("release lease after cancellation: %v", err)
	}
	var count int
	if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&count); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if count != 0 {
		t.Fatalf("lease rows = %d, want 0", count)
	}
}

func openLeaseTestDatabase(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	database.SetMaxOpenConns(1)
	if err := auditstorage.Migrate(t.Context(), database); err != nil {
		_ = database.Close()
		t.Fatalf("Migrate: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}
