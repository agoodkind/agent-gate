package auditstorage

import (
	"context"
	"database/sql"
)

func migrateMaintenanceV5(ctx context.Context, transaction *sql.Tx) error {
	return executeStatements(ctx, transaction, []string{
		`create table if not exists audit_maintenance_lease (
			singleton integer primary key check(singleton = 1),
			owner text not null,
			run_id text not null,
			expires_at text not null
		)`,
		`create table if not exists audit_maintenance_runs (
			run_id text primary key,
			planned_at text not null,
			started_at text not null,
			completed_at text,
			policy_hash text not null,
			plan_json text not null,
			detail_graphs integer not null default 0,
			summary_graphs integer not null default 0,
			reclaimed_bytes integer not null default 0,
			result text not null,
			error_class text not null default '',
			next_due_at text
		)`,
	})
}
