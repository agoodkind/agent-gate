package auditstorage

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

const legacyOrphanEvaluationPredicate = `
	not exists (
		select 1 from intake_events event
		where event.event_id = evaluation.event_id
	)
	or not exists (
		select 1 from intake_receipts receipt
		where receipt.receipt_id = evaluation.receipt_id
			and receipt.event_id = evaluation.event_id
	)`

// LegacyQuarantineSummary reports preserved pre-versioned orphan rows.
type LegacyQuarantineSummary struct {
	EvaluationCount  int
	LayerCount       int
	LabelCount       int
	OutboxCount      int
	OutboxEntryCount int
}

var legacyQuarantineSchema = []string{
	`create table if not exists audit_migration_quarantined_evaluations as
	select evaluation.rowid as source_rowid, cast('' as text) as reason,
		evaluation.*
	from gate_evaluations evaluation
	where false`,
	`create unique index if not exists audit_migration_quarantined_evaluations_id_idx
		on audit_migration_quarantined_evaluations(evaluation_id)`,
	`create table if not exists audit_migration_quarantined_evaluation_layers as
	select layer.* from gate_evaluation_layers layer where false`,
	`create unique index if not exists audit_migration_quarantined_layers_id_idx
		on audit_migration_quarantined_evaluation_layers(evaluation_id, layer_index)`,
	`create table if not exists audit_migration_quarantined_evaluation_labels as
	select label.* from gate_evaluation_labels label where false`,
	`create unique index if not exists audit_migration_quarantined_labels_id_idx
		on audit_migration_quarantined_evaluation_labels(
			evaluation_id, namespace, label_version
		)`,
	`create table if not exists audit_migration_quarantined_outbox as
	select outbox.* from deferred_audit_outbox outbox where false`,
	`create unique index if not exists audit_migration_quarantined_outbox_id_idx
		on audit_migration_quarantined_outbox(receipt_id)`,
	`create table if not exists audit_migration_quarantined_outbox_entries as
	select entry.* from deferred_audit_outbox_entries entry where false`,
	`create unique index if not exists audit_migration_quarantined_entries_id_idx
		on audit_migration_quarantined_outbox_entries(receipt_id, entry_index)`,
}

var legacyQuarantineCopies = []string{
	`insert into audit_migration_quarantined_evaluations
	select evaluation.rowid,
		case
			when not exists (
				select 1 from intake_events event
				where event.event_id = evaluation.event_id
			) and not exists (
				select 1 from intake_receipts receipt
				where receipt.receipt_id = evaluation.receipt_id
					and receipt.event_id = evaluation.event_id
			) then 'missing intake event and receipt'
			when not exists (
				select 1 from intake_events event
				where event.event_id = evaluation.event_id
			) then 'missing intake event'
			else 'missing intake receipt'
		end,
		evaluation.*
	from gate_evaluations evaluation
	where ` + legacyOrphanEvaluationPredicate,
	`insert into audit_migration_quarantined_evaluation_layers
	select layer.*
	from gate_evaluation_layers layer
	join audit_migration_quarantined_evaluations quarantine
		using (evaluation_id)`,
	`insert into audit_migration_quarantined_evaluation_labels
	select label.*
	from gate_evaluation_labels label
	join audit_migration_quarantined_evaluations quarantine
		using (evaluation_id)`,
	`insert into audit_migration_quarantined_outbox_entries
	select entry.*
	from deferred_audit_outbox_entries entry
	join deferred_audit_outbox outbox using (receipt_id)
	join audit_migration_quarantined_evaluations quarantine
		using (evaluation_id)`,
	`insert into audit_migration_quarantined_outbox
	select outbox.*
	from deferred_audit_outbox outbox
	join audit_migration_quarantined_evaluations quarantine
		using (evaluation_id)`,
}

// LegacyQuarantine preserves recognized pre-versioned evaluation orphans.
func LegacyQuarantine(
	ctx context.Context,
	transaction *sql.Tx,
) (LegacyQuarantineSummary, error) {
	var emptySummary LegacyQuarantineSummary
	if transaction == nil {
		return emptySummary, fmt.Errorf("legacy quarantine transaction is required")
	}
	orphanCount, err := countLegacyOrphanEvaluations(ctx, transaction)
	if err != nil {
		return emptySummary, err
	}
	if orphanCount == 0 {
		return emptySummary, nil
	}
	if err := executeStatements(ctx, transaction, legacyQuarantineSchema); err != nil {
		return emptySummary, err
	}
	if err := executeStatements(ctx, transaction, legacyQuarantineCopies); err != nil {
		return emptySummary, err
	}
	summary, err := readLegacyQuarantineSummary(ctx, transaction)
	if err != nil {
		return emptySummary, err
	}
	if err := verifyLegacyQuarantineCounts(ctx, transaction, summary); err != nil {
		return emptySummary, err
	}
	if _, err := transaction.ExecContext(ctx, `
		delete from deferred_audit_outbox
		where evaluation_id in (
			select evaluation_id
			from audit_migration_quarantined_evaluations
		)
	`); err != nil {
		return emptySummary, wrapError("remove quarantined audit outbox", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		delete from gate_evaluations
		where evaluation_id in (
			select evaluation_id
			from audit_migration_quarantined_evaluations
		)
	`); err != nil {
		return emptySummary, wrapError("remove quarantined evaluations", err)
	}
	return summary, nil
}

func countLegacyOrphanEvaluations(ctx context.Context, transaction *sql.Tx) (int, error) {
	var count int
	if err := transaction.QueryRowContext(ctx, `
		select count(*) from gate_evaluations evaluation
		where `+legacyOrphanEvaluationPredicate,
	).Scan(&count); err != nil {
		return 0, wrapError("count legacy orphan evaluations", err)
	}
	return count, nil
}

func readLegacyQuarantineSummary(
	ctx context.Context,
	transaction *sql.Tx,
) (LegacyQuarantineSummary, error) {
	return scanLegacyQuarantineSummary(transaction.QueryRowContext(ctx, legacyQuarantineSummaryQuery))
}

func readCommittedLegacyQuarantineSummary(
	ctx context.Context,
	connection *sql.Conn,
) (LegacyQuarantineSummary, error) {
	return scanLegacyQuarantineSummary(connection.QueryRowContext(ctx, legacyQuarantineSummaryQuery))
}

const legacyQuarantineSummaryQuery = `
	select
		(select count(*) from audit_migration_quarantined_evaluations),
		(select count(*) from audit_migration_quarantined_evaluation_layers),
		(select count(*) from audit_migration_quarantined_evaluation_labels),
		(select count(*) from audit_migration_quarantined_outbox),
		(select count(*) from audit_migration_quarantined_outbox_entries)
`

func scanLegacyQuarantineSummary(row *sql.Row) (LegacyQuarantineSummary, error) {
	var summary LegacyQuarantineSummary
	if err := row.Scan(
		&summary.EvaluationCount,
		&summary.LayerCount,
		&summary.LabelCount,
		&summary.OutboxCount,
		&summary.OutboxEntryCount,
	); err != nil {
		return summary, wrapError("read legacy quarantine summary", err)
	}
	return summary, nil
}

func verifyLegacyQuarantineCounts(
	ctx context.Context,
	transaction *sql.Tx,
	summary LegacyQuarantineSummary,
) error {
	var source LegacyQuarantineSummary
	if err := transaction.QueryRowContext(ctx, `
		select
			(select count(*) from gate_evaluations evaluation
				where `+legacyOrphanEvaluationPredicate+`),
			(select count(*) from gate_evaluation_layers layer
				join audit_migration_quarantined_evaluations quarantine
					using (evaluation_id)),
			(select count(*) from gate_evaluation_labels label
				join audit_migration_quarantined_evaluations quarantine
					using (evaluation_id)),
			(select count(*) from deferred_audit_outbox outbox
				join audit_migration_quarantined_evaluations quarantine
					using (evaluation_id)),
			(select count(*) from deferred_audit_outbox_entries entry
				join deferred_audit_outbox outbox using (receipt_id)
				join audit_migration_quarantined_evaluations quarantine
					using (evaluation_id))
	`).Scan(
		&source.EvaluationCount,
		&source.LayerCount,
		&source.LabelCount,
		&source.OutboxCount,
		&source.OutboxEntryCount,
	); err != nil {
		return wrapError("verify legacy quarantine source counts", err)
	}
	if source != summary {
		return fmt.Errorf("legacy quarantine copied %+v rows from %+v source rows", summary, source)
	}
	return nil
}

func reportLegacyQuarantine(ctx context.Context, connection *sql.Conn) {
	var tableCount int
	if err := connection.QueryRowContext(ctx, `
		select count(*) from sqlite_schema
		where type = 'table'
			and name = 'audit_migration_quarantined_evaluations'
	`).Scan(&tableCount); err != nil {
		slog.ErrorContext(ctx, "inspect committed legacy audit quarantine failed", "err", err)
		return
	}
	if tableCount == 0 {
		return
	}
	summary, err := readCommittedLegacyQuarantineSummary(ctx, connection)
	if err != nil {
		slog.ErrorContext(ctx, "read committed legacy audit quarantine failed", "err", err)
		return
	}
	if summary.EvaluationCount == 0 {
		return
	}
	slog.WarnContext(
		ctx,
		"preserved legacy audit evaluations with missing intake parents",
		"evaluations", summary.EvaluationCount,
		"layers", summary.LayerCount,
		"labels", summary.LabelCount,
		"outboxes", summary.OutboxCount,
		"outbox_entries", summary.OutboxEntryCount,
	)
}
