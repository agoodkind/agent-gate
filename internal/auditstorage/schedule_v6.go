package auditstorage

import (
	"context"
	"database/sql"
)

func migrateScheduleV6(ctx context.Context, transaction *sql.Tx) error {
	return executeStatements(ctx, transaction, []string{
		`create table if not exists audit_maintenance_schedule (
			singleton integer primary key check(singleton = 1),
			next_attempt_at text not null
		)`,
	})
}
