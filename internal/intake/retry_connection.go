package intake

import (
	"context"
	_ "embed"
	"errors"

	"goodkind.io/agent-gate/internal/auditstorage"
)

//go:embed retry_begin.sql
var retryBeginSQL string

//go:embed retry_rollback.sql
var retryRollbackSQL string

//go:embed retry_commit.sql
var retryCommitSQL string

//go:embed retry_read.sql
var retryReadSQL string

func (s *Store) retryConnection(ctx context.Context) (*auditstorage.WriterConnection, func() error, error) {
	connection, err := auditstorage.BorrowWriterConnection(ctx, s.db)
	if err != nil {
		return nil, nil, wrapLoggedError(ctx, s.log, "borrow deferred retry connection", err)
	}
	release := func() error {
		cleanupContext := context.WithoutCancel(ctx)
		if connection.InTransaction() {
			if _, err := connection.ExecContext(cleanupContext, retryRollbackSQL); err != nil {
				return wrapLoggedError(cleanupContext, s.log, "rollback deferred retry", errors.Join(err, connection.Discard()))
			}
		}
		return wrapLoggedError(cleanupContext, s.log, "release deferred retry connection", connection.Close())
	}
	if _, err := connection.ExecContext(ctx, retryBeginSQL); err != nil {
		return nil, nil, wrapLoggedError(ctx, s.log, "begin deferred retry", errors.Join(err, release()))
	}
	return connection, release, nil
}
