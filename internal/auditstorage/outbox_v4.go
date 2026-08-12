package auditstorage

import (
	"context"
	"database/sql"
)

var outboxV4DetailSchema = []string{
	`create table if not exists deferred_audit_outbox_entry_details (
		receipt_id integer not null,
		entry_index integer not null,
		payload_json blob not null,
		primary key(receipt_id, entry_index),
		foreign key(receipt_id, entry_index)
			references deferred_audit_outbox_entries(receipt_id, entry_index)
			on delete cascade
	)`,
}

var outboxV4Statements = []string{
	`insert into deferred_audit_outbox_entry_details (
		receipt_id, entry_index, payload_json
	)
	select receipt_id, entry_index, payload_json
	from deferred_audit_outbox_entries`,
	`create table deferred_audit_outbox_entries_v4 (
		receipt_id integer not null,
		entry_index integer not null,
		audit_event_id text not null,
		delivered_at text,
		payload_recorded integer not null,
		payload_available integer not null,
		payload_state_changed_at text not null,
		primary key(receipt_id, entry_index),
		foreign key(receipt_id) references deferred_audit_outbox(receipt_id)
			on delete cascade,
		check(payload_recorded in (0, 1)),
		check(payload_available in (0, 1))
	)`,
	`insert into deferred_audit_outbox_entries_v4 (
		receipt_id, entry_index, audit_event_id, delivered_at,
		payload_recorded, payload_available, payload_state_changed_at
	)
	select entry.receipt_id, entry.entry_index, entry.audit_event_id,
		entry.delivered_at, 1, 1,
		coalesce(entry.delivered_at, outbox.completed_at, outbox.created_at)
	from deferred_audit_outbox_entries entry
	join deferred_audit_outbox outbox on outbox.receipt_id = entry.receipt_id`,
	`drop table deferred_audit_outbox_entries`,
	`alter table deferred_audit_outbox_entries_v4
		rename to deferred_audit_outbox_entries`,
}

func migrateOutboxV4(ctx context.Context, transaction *sql.Tx) error {
	hasCompatibilityPayload, err := columnExists(
		ctx,
		transaction,
		"deferred_audit_outbox_entries",
		"payload_json",
	)
	if err != nil {
		return err
	}
	if !hasCompatibilityPayload {
		return executeStatements(ctx, transaction, outboxV4DetailSchema)
	}
	if err := executeStatements(ctx, transaction, outboxV4DetailSchema); err != nil {
		return err
	}
	return executeStatements(ctx, transaction, outboxV4Statements)
}
