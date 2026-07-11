package intake

import (
	"context"
	"database/sql"
	"log/slog"
)

func ensureDeferredReceiptSchema(ctx context.Context, database *sql.DB) error {
	hasReceiptID, err := tableHasColumn(ctx, database, "intake_deferred", "receipt_id")
	if err != nil {
		return err
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return wrapError("begin deferred receipt migration", err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()
	if _, err := transaction.ExecContext(ctx, `
		create unique index if not exists intake_receipts_identity_idx
		on intake_receipts(receipt_id, event_id)
	`); err != nil {
		return wrapError("create intake receipt identity index", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		create table if not exists intake_deferred_repairs (
			event_id text primary key,
			state text not null,
			pending_at text,
			completed_at text,
			last_replay_at text,
			replay_count integer not null default 0,
			repair_error text not null
		)
	`); err != nil {
		return wrapError("create deferred repair table", err)
	}
	if !hasReceiptID {
		if err := migrateLegacyDeferredRows(ctx, transaction); err != nil {
			return err
		}
	}
	if _, err := transaction.ExecContext(ctx, `
		create index if not exists intake_deferred_state_idx on intake_deferred(state);
		create index if not exists intake_deferred_event_id_idx on intake_deferred(event_id);
	`); err != nil {
		return wrapError("create deferred receipt indexes", err)
	}
	if err := transaction.Commit(); err != nil {
		return wrapError("commit deferred receipt migration", err)
	}
	return nil
}

func migrateLegacyDeferredRows(ctx context.Context, transaction *sql.Tx) error {
	statements := []string{
		`alter table intake_deferred rename to intake_deferred_legacy`,
		`create table intake_deferred (
			receipt_id integer primary key,
			event_id text not null,
			state text not null,
			pending_at text,
			completed_at text,
			last_replay_at text,
			replay_count integer not null default 0,
			foreign key(receipt_id, event_id)
				references intake_receipts(receipt_id, event_id) on delete cascade,
			check(state in ('none', 'pending', 'complete'))
		)`,
		`insert into intake_deferred (
			receipt_id, event_id, state, pending_at, completed_at,
			last_replay_at, replay_count
		)
		select r.receipt_id, legacy.event_id, legacy.state, legacy.pending_at,
			legacy.completed_at, legacy.last_replay_at, legacy.replay_count
		from intake_deferred_legacy legacy
		join intake_receipts r on r.receipt_id = (
			select max(candidate.receipt_id)
			from intake_receipts candidate
			where candidate.event_id = legacy.event_id
		)`,
		`insert or replace into intake_deferred_repairs (
			event_id, state, pending_at, completed_at, last_replay_at,
			replay_count, repair_error
		)
		select legacy.event_id, legacy.state, legacy.pending_at,
			legacy.completed_at, legacy.last_replay_at, legacy.replay_count,
			'missing_receipt'
		from intake_deferred_legacy legacy
		where not exists (
			select 1 from intake_receipts receipt
			where receipt.event_id = legacy.event_id
		)`,
		`drop table intake_deferred_legacy`,
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return wrapError("migrate legacy deferred rows", err)
		}
	}
	return nil
}

func tableHasColumn(ctx context.Context, database *sql.DB, tableName string, columnName string) (bool, error) {
	rows, err := database.QueryContext(ctx, "pragma table_info("+tableName+")")
	if err != nil {
		return false, wrapError("query "+tableName+" schema", err)
	}
	defer func() {
		_ = rows.Close()
	}()
	for rows.Next() {
		var columnID int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&columnID, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, wrapError("scan "+tableName+" schema", err)
		}
		if name == columnName {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, wrapError("iterate "+tableName+" schema", err)
	}
	return false, nil
}

func logDeferredReceiptRepairs(ctx context.Context, database *sql.DB, log *slog.Logger) {
	if log == nil {
		return
	}
	var repairCount int
	err := database.QueryRowContext(ctx, `
		select count(*) from intake_deferred_repairs
		where state = ? and repair_error = 'missing_receipt'
	`, DeferredStatePending).Scan(&repairCount)
	if err != nil || repairCount == 0 {
		return
	}
	log.WarnContext(
		ctx,
		"legacy deferred rows require receipt repair",
		"repair_error", "missing_receipt",
		"repair_count", repairCount,
	)
}
