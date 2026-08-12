package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// WriteNextAttempt replaces the scheduler deadline without changing maintenance runs.
func WriteNextAttempt(ctx context.Context, path string, nextAttempt time.Time) (returnErr error) {
	if strings.TrimSpace(path) == "" {
		return errors.New("audit database path is required")
	}
	if nextAttempt.IsZero() {
		return errors.New("audit maintenance next attempt is required")
	}
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		return wrapError("open audit maintenance schedule database", err)
	}
	database.SetMaxOpenConns(1)
	defer func() {
		if err := database.Close(); err != nil {
			returnErr = errors.Join(returnErr, wrapError("close audit maintenance schedule database", err))
		}
	}()
	if err := auditstorage.MigrateNonblocking(ctx, database); err != nil {
		return wrapError("migrate audit maintenance schedule database", err)
	}
	if _, err := database.ExecContext(ctx, `
		insert into audit_maintenance_schedule(singleton, next_attempt_at)
		values (1, ?)
		on conflict(singleton) do update set next_attempt_at = excluded.next_attempt_at
	`, nextAttempt.UTC().Format(time.RFC3339Nano)); err != nil {
		return wrapError("write audit maintenance next attempt", err)
	}
	return nil
}
