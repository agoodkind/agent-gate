package intake

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"errors"
	"fmt"
)

//go:embed retry_busy_timeout.sql
var retryBusyTimeoutSQL string

//go:embed retry_no_wait.sql
var retryNoWaitSQL string

//go:embed retry_restore_timeout.sql
var retryRestoreTimeoutSQL string

func (s *Store) retryConnection(ctx context.Context) (*sql.Conn, func() error, error) {
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return nil, nil, wrapLoggedError(ctx, s.log, "borrow deferred retry connection", err)
	}
	var timeout int
	if err := connection.QueryRowContext(ctx, retryBusyTimeoutSQL).Scan(&timeout); err != nil {
		return nil, nil, wrapLoggedError(ctx, s.log, "read deferred retry busy timeout", errors.Join(err, connection.Close()))
	}
	release := func() error {
		restoreContext := context.WithoutCancel(ctx)
		_, restoreError := connection.ExecContext(restoreContext, fmt.Sprintf(retryRestoreTimeoutSQL, timeout))
		if restoreError == nil {
			return wrapLoggedError(restoreContext, s.log, "release deferred retry connection", connection.Close())
		}
		restoreError = fmt.Errorf("restore deferred retry busy timeout: %w", restoreError)
		s.log.WarnContext(restoreContext, "restore deferred retry busy timeout failed", "err", restoreError)
		// database/sql requires an untyped callback to discard the physical connection.
		discardError := connection.Raw(func(_ any) error { return driver.ErrBadConn })
		if errors.Is(discardError, driver.ErrBadConn) || errors.Is(discardError, sql.ErrConnDone) {
			discardError = nil
		}
		return errors.Join(
			restoreError,
			wrapLoggedError(restoreContext, s.log, "discard deferred retry connection", discardError),
		)
	}
	if _, err := connection.ExecContext(ctx, retryNoWaitSQL); err != nil {
		return nil, nil, wrapLoggedError(ctx, s.log, "set deferred retry busy timeout", errors.Join(err, release()))
	}
	return connection, release, nil
}
