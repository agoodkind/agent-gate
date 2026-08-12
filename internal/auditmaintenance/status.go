package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

const oldestGraphTimesQuery = `
with latest_graph_receipts as (
	select event.event_id, receipt.received_at,
		row_number() over (
			partition by event.event_id
			order by julianday(receipt.received_at) desc, receipt.receipt_id desc
		) as receipt_rank
	from intake_events event
	join intake_receipts receipt on receipt.event_id = event.event_id
),
event_graphs as (
	select event_id, received_at as latest_received_at
	from latest_graph_receipts
	where receipt_rank = 1
)
select
	(select graph.latest_received_at
	from event_graphs graph
	where exists (select 1 from intake_event_details detail
			where detail.event_id = graph.event_id)
		or exists (select 1 from gate_evaluation_details detail
			join gate_evaluations evaluation using (evaluation_id)
			where evaluation.event_id = graph.event_id)
		or exists (select 1 from gate_evaluation_layer_details detail
			join gate_evaluations evaluation using (evaluation_id)
			where evaluation.event_id = graph.event_id)
		or exists (select 1 from gate_evaluation_label_details detail
			join gate_evaluations evaluation using (evaluation_id)
			where evaluation.event_id = graph.event_id)
		or exists (select 1 from deferred_audit_outbox_entry_details detail
			join deferred_audit_outbox outbox using (receipt_id)
			where outbox.event_id = graph.event_id)
	order by julianday(graph.latest_received_at), graph.event_id
	limit 1),
	(select graph.latest_received_at
	from event_graphs graph
	order by julianday(graph.latest_received_at), graph.event_id
	limit 1)
`

// ReadStatus inspects current audit storage without creating metadata.
func ReadStatus(
	ctx context.Context,
	path string,
	policy config.AuditStoragePolicy,
	now time.Time,
) (Status, error) {
	snapshot, err := openDatabaseSnapshot(ctx, path)
	if err != nil {
		return Status{}, err
	}
	defer snapshot.cleanup()
	plan, err := previewDatabase(ctx, snapshot.database, policy, now)
	if err != nil {
		return Status{}, err
	}

	status := Status{
		Policy: policy, DatabaseBytes: 0, WALBytes: 0,
		OldestDetailAt: nil, OldestSummaryAt: nil,
		ProtectedGraphs: plan.ProtectedGraphs, ReclaimablePages: 0,
		FullCompactNeeded: false, IntegrityOK: false, IntegrityError: "",
		LastRun: nil, MaintenanceDueAt: nil, NextAttemptAt: nil,
		Overdue: false, SizeState: SizeStateDisabled,
	}
	status.DatabaseBytes = snapshot.databaseBytes
	status.WALBytes = snapshot.walBytes
	if err := readGraphTimes(ctx, snapshot.database, &status); err != nil {
		return Status{}, err
	}
	compactedUsageBytes, err := readPageState(ctx, snapshot.database, &status)
	if err != nil {
		return Status{}, err
	}
	if err := readIntegrity(ctx, snapshot.database, &status); err != nil {
		return Status{}, err
	}
	if err := readMaintenanceMetadata(ctx, snapshot.database, policy, &status); err != nil {
		return Status{}, err
	}
	eligibleGraphs, err := readEligibleGraphCount(ctx, snapshot.database)
	if err != nil {
		return Status{}, err
	}
	protectedCompactedBytes := int64(0)
	if policy.MaxSizeBytes > 0 && plan.ProtectedGraphs > 0 && eligibleGraphs > 0 &&
		compactedUsageBytes > policy.MaxSizeBytes {
		protectedCompactedBytes, err = measureProtectedCompactedUsage(
			ctx,
			snapshot.database,
		)
		if err != nil {
			return Status{}, err
		}
	}
	status.SizeState = classifySize(
		status,
		compactedUsageBytes,
		eligibleGraphs,
		protectedCompactedBytes,
	)
	if status.MaintenanceDueAt != nil {
		status.Overdue = now.UTC().After(*status.MaintenanceDueAt)
	}
	return status, nil
}

func readGraphTimes(ctx context.Context, database *sql.DB, status *Status) error {
	var oldestDetail sql.NullString
	var oldestSummary sql.NullString
	if err := database.QueryRowContext(ctx, oldestGraphTimesQuery).Scan(
		&oldestDetail,
		&oldestSummary,
	); err != nil {
		return wrapError("read oldest audit graph times", err)
	}
	var err error
	status.OldestDetailAt, err = parseNullableTime(oldestDetail)
	if err != nil {
		return wrapError("parse oldest audit detail time", err)
	}
	status.OldestSummaryAt, err = parseNullableTime(oldestSummary)
	if err != nil {
		return wrapError("parse oldest audit summary time", err)
	}
	return nil
}

func readPageState(
	ctx context.Context,
	database *sql.DB,
	status *Status,
) (int64, error) {
	var busy int64
	var walFrames int64
	var checkpointedFrames int64
	if err := database.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&busy,
		&walFrames,
		&checkpointedFrames,
	); err != nil {
		return 0, wrapError("measure audit live write-ahead log frames", err)
	}
	if err := database.QueryRowContext(ctx, `pragma freelist_count`).Scan(
		&status.ReclaimablePages,
	); err != nil {
		return 0, wrapError("read audit reclaimable pages", err)
	}
	var pageSize int64
	var pageCount int64
	if err := database.QueryRowContext(ctx, `pragma page_size`).Scan(&pageSize); err != nil {
		return 0, wrapError("read audit page size", err)
	}
	if err := database.QueryRowContext(ctx, `pragma page_count`).Scan(&pageCount); err != nil {
		return 0, wrapError("read audit page count", err)
	}
	var autoVacuumMode int
	if err := database.QueryRowContext(ctx, `pragma auto_vacuum`).Scan(&autoVacuumMode); err != nil {
		return 0, wrapError("read audit auto-vacuum mode", err)
	}
	status.FullCompactNeeded = autoVacuumMode != 2
	liveFrames := max(walFrames-checkpointedFrames, 0)
	return (pageCount-status.ReclaimablePages)*pageSize + liveFrames*pageSize, nil
}

func readIntegrity(ctx context.Context, database *sql.DB, status *Status) error {
	rows, err := database.QueryContext(ctx, `pragma quick_check`)
	if err != nil {
		return wrapError("check audit database integrity", err)
	}
	defer func() { _ = rows.Close() }()
	problems := make([]string, 0)
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return wrapError("scan audit database integrity", err)
		}
		if result != "ok" {
			problems = append(problems, result)
		}
	}
	if err := rows.Err(); err != nil {
		return wrapError("iterate audit database integrity", err)
	}
	status.IntegrityOK = len(problems) == 0
	status.IntegrityError = strings.Join(problems, "; ")
	return nil
}

func readMaintenanceMetadata(
	ctx context.Context,
	database *sql.DB,
	policy config.AuditStoragePolicy,
	status *Status,
) error {
	runsExist, err := tableExists(ctx, database, "audit_maintenance_runs")
	if err != nil {
		return err
	}
	if runsExist {
		status.LastRun, err = readLastRun(ctx, database)
		if err != nil {
			return err
		}
	}
	baseTime, err := maintenanceBaseTime(ctx, database, runsExist)
	if err != nil {
		return err
	}
	if baseTime != nil && policy.MaintenanceInterval > 0 {
		dueAt := baseTime.Add(policy.MaintenanceInterval)
		status.MaintenanceDueAt = &dueAt
	}
	status.NextAttemptAt, err = readNextAttempt(ctx, database)
	return err
}

func readLastRun(ctx context.Context, database *sql.DB) (*RunSummary, error) {
	var run RunSummary
	var plannedAt string
	var startedAt string
	var completedAt sql.NullString
	var nextDueAt sql.NullString
	err := database.QueryRowContext(ctx, `
		select run_id, planned_at, started_at, completed_at, policy_hash,
			detail_graphs, summary_graphs, reclaimed_bytes, result,
			error_class, next_due_at
		from audit_maintenance_runs
		order by started_at desc limit 1
	`).Scan(
		&run.RunID,
		&plannedAt,
		&startedAt,
		&completedAt,
		&run.PolicyHash,
		&run.DetailGraphs,
		&run.SummaryGraphs,
		&run.ReclaimedBytes,
		&run.Result,
		&run.ErrorClass,
		&nextDueAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapError("read last audit maintenance run", err)
	}
	if run.PlannedAt, err = time.Parse(time.RFC3339Nano, plannedAt); err != nil {
		return nil, wrapError("parse maintenance planned time", err)
	}
	if run.StartedAt, err = time.Parse(time.RFC3339Nano, startedAt); err != nil {
		return nil, wrapError("parse maintenance start time", err)
	}
	if run.CompletedAt, err = parseNullableTime(completedAt); err != nil {
		return nil, wrapError("parse maintenance completion time", err)
	}
	if run.NextDueAt, err = parseNullableTime(nextDueAt); err != nil {
		return nil, wrapError("parse maintenance due time", err)
	}
	return &run, nil
}

func maintenanceBaseTime(
	ctx context.Context,
	database *sql.DB,
	runsExist bool,
) (*time.Time, error) {
	if runsExist {
		var completedAt string
		err := database.QueryRowContext(ctx, `
			select completed_at
			from audit_maintenance_runs
			where result = 'success' and completed_at is not null
			order by completed_at desc limit 1
		`).Scan(&completedAt)
		if err == nil {
			parsed, parseErr := time.Parse(time.RFC3339Nano, completedAt)
			if parseErr != nil {
				return nil, wrapError("parse last successful maintenance time", parseErr)
			}
			return &parsed, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, wrapError("read last successful maintenance time", err)
		}
	}
	var appliedAt string
	err := database.QueryRowContext(ctx, `
		select applied_at from audit_schema_migrations order by version desc limit 1
	`).Scan(&appliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, wrapError("read audit storage migration time", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, appliedAt)
	if err != nil {
		return nil, wrapError("parse audit storage migration time", err)
	}
	return &parsed, nil
}

func readNextAttempt(ctx context.Context, database *sql.DB) (*time.Time, error) {
	tables := []struct {
		name  string
		query string
	}{
		{name: "audit_maintenance_schedule", query: "select next_attempt_at from audit_maintenance_schedule limit 1"},
		{name: "audit_maintenance_scheduler", query: "select next_attempt_at from audit_maintenance_scheduler limit 1"},
	}
	for _, table := range tables {
		exists, err := tableExists(ctx, database, table.name)
		if err != nil {
			return nil, err
		}
		if !exists {
			continue
		}
		var raw sql.NullString
		if err := database.QueryRowContext(ctx, table.query).Scan(&raw); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil
			}
			return nil, wrapError("read next audit maintenance attempt", err)
		}
		parsed, err := parseNullableTime(raw)
		if err != nil {
			return nil, wrapError("parse next audit maintenance attempt", err)
		}
		return parsed, nil
	}
	return nil, nil
}

func tableExists(ctx context.Context, database *sql.DB, table string) (bool, error) {
	var count int
	if err := database.QueryRowContext(
		ctx,
		`select count(*) from sqlite_schema where type = 'table' and name = ?`,
		table,
	).Scan(&count); err != nil {
		return false, wrapError(fmt.Sprintf("inspect audit table %q", table), err)
	}
	return count != 0, nil
}

func parseNullableTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw.String)
	if err != nil {
		return nil, wrapError("parse audit maintenance time", err)
	}
	return &parsed, nil
}

func readEligibleGraphCount(ctx context.Context, database *sql.DB) (int64, error) {
	var count int64
	if err := database.QueryRowContext(ctx, `
		with event_graphs as (
			select event.event_id,
				exists (
					select 1 from intake_receipts receipt
					where receipt.event_id = event.event_id and (
						not exists (select 1 from gate_evaluations evaluation
							where evaluation.receipt_id = receipt.receipt_id
								and evaluation.mode = 'hot')
						or exists (select 1 from intake_deferred deferred
							where deferred.receipt_id = receipt.receipt_id
								and deferred.state = 'pending')
						or exists (select 1 from deferred_audit_outbox outbox
							where outbox.receipt_id = receipt.receipt_id
								and outbox.state = 'pending')
						or exists (select 1 from deferred_audit_outbox_entries entry
							where entry.receipt_id = receipt.receipt_id
								and entry.delivered_at is null)
					)
				) as protected
			from intake_events event
		)
		select count(*) from event_graphs where protected = 0
	`).Scan(&count); err != nil {
		return 0, wrapError("count size-eligible audit graphs", err)
	}
	return count, nil
}

func measureProtectedCompactedUsage(ctx context.Context, database *sql.DB) (int64, error) {
	slog.DebugContext(ctx, "measure protected compacted audit usage")
	if _, err := database.ExecContext(ctx, `pragma foreign_keys = off`); err != nil {
		return 0, wrapError("disable snapshot foreign keys for protected size measurement", err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, wrapError("begin protected size measurement", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, protectedCompactionStatements); err != nil {
		return 0, wrapError("remove eligible graphs from protected size snapshot", err)
	}
	if err := transaction.Commit(); err != nil {
		return 0, wrapError("commit protected size snapshot", err)
	}
	if _, err := database.ExecContext(ctx, `pragma wal_checkpoint(truncate)`); err != nil {
		return 0, wrapError("checkpoint protected size snapshot", err)
	}
	if _, err := database.ExecContext(ctx, `pragma journal_mode = delete`); err != nil {
		return 0, wrapError("disable snapshot write-ahead log", err)
	}
	if _, err := database.ExecContext(ctx, `vacuum`); err != nil {
		return 0, wrapError("compact protected size snapshot", err)
	}
	var pageSize int64
	var pageCount int64
	if err := database.QueryRowContext(ctx, `pragma page_size`).Scan(&pageSize); err != nil {
		return 0, wrapError("read protected snapshot page size", err)
	}
	if err := database.QueryRowContext(ctx, `pragma page_count`).Scan(&pageCount); err != nil {
		return 0, wrapError("read protected snapshot page count", err)
	}
	return pageSize * pageCount, nil
}

const protectedCompactionStatements = `
create temp table eligible_event_ids as
select event.event_id
from intake_events event
where not exists (
	select 1 from intake_receipts receipt
	where receipt.event_id = event.event_id and (
		not exists (select 1 from gate_evaluations evaluation
			where evaluation.receipt_id = receipt.receipt_id
				and evaluation.mode = 'hot')
		or exists (select 1 from intake_deferred deferred
			where deferred.receipt_id = receipt.receipt_id
				and deferred.state = 'pending')
		or exists (select 1 from deferred_audit_outbox outbox
			where outbox.receipt_id = receipt.receipt_id
				and outbox.state = 'pending')
		or exists (select 1 from deferred_audit_outbox_entries entry
			where entry.receipt_id = receipt.receipt_id
				and entry.delivered_at is null)
	)
);
create temp table eligible_receipt_ids as
select receipt_id from intake_receipts
where event_id in (select event_id from eligible_event_ids);
create temp table eligible_evaluation_ids as
select evaluation_id from gate_evaluations
where event_id in (select event_id from eligible_event_ids);
create temp table eligible_audit_event_ids as
select distinct eligible_entry.audit_event_id
from deferred_audit_outbox_entries eligible_entry
where eligible_entry.receipt_id in (select receipt_id from eligible_receipt_ids)
	and not exists (
		select 1 from deferred_audit_outbox_entries protected_entry
		where protected_entry.audit_event_id = eligible_entry.audit_event_id
			and protected_entry.receipt_id not in (
				select receipt_id from eligible_receipt_ids
			)
	);
delete from violations where event_id in (select audit_event_id from eligible_audit_event_ids);
delete from decisions where event_id in (select audit_event_id from eligible_audit_event_ids);
delete from operations where event_id in (select audit_event_id from eligible_audit_event_ids);
delete from events where event_id in (select audit_event_id from eligible_audit_event_ids);
delete from deferred_audit_outbox_entry_details
where receipt_id in (select receipt_id from eligible_receipt_ids);
delete from deferred_audit_outbox_entries
where receipt_id in (select receipt_id from eligible_receipt_ids);
delete from deferred_audit_outbox
where receipt_id in (select receipt_id from eligible_receipt_ids);
delete from gate_evaluation_label_details
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from gate_evaluation_labels
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from gate_evaluation_layer_details
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from gate_evaluation_layers
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from gate_evaluation_details
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from gate_evaluations
where evaluation_id in (select evaluation_id from eligible_evaluation_ids);
delete from intake_deferred
where receipt_id in (select receipt_id from eligible_receipt_ids);
delete from intake_event_details
where event_id in (select event_id from eligible_event_ids);
delete from intake_event_detail_manifest
where event_id in (select event_id from eligible_event_ids);
delete from intake_receipts
where receipt_id in (select receipt_id from eligible_receipt_ids);
delete from intake_events
where event_id in (select event_id from eligible_event_ids);
`

func classifySize(
	status Status,
	compactedUsageBytes int64,
	eligibleGraphs int64,
	protectedCompactedBytes int64,
) SizeState {
	if status.Policy.MaxSizeBytes == 0 {
		return SizeStateDisabled
	}
	physicalBytes := status.DatabaseBytes + status.WALBytes
	if compactedUsageBytes <= status.Policy.MaxSizeBytes {
		if physicalBytes > status.Policy.MaxSizeBytes {
			return SizeStateReclaimPending
		}
		return SizeStateWithinTarget
	}
	if eligibleGraphs == 0 && status.ProtectedGraphs > 0 {
		return SizeStateConstrained
	}
	if protectedCompactedBytes > status.Policy.MaxSizeBytes {
		return SizeStateConstrained
	}
	return SizeStateOverTarget
}
