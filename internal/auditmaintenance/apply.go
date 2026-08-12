package auditmaintenance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
)

const (
	logAuditSizeMeasurementFailed   = "audit size measurement failed"
	logMeasureAuditSize             = "measure audit size"
	logMaintenanceStartRecordFailed = "audit maintenance start record failed"
	logRecordMaintenanceStart       = "record audit maintenance start"
	runIDBytes                      = 16
)

var maintenanceNow = time.Now

// ApplyOptions describes one immutable online maintenance run.
type ApplyOptions struct {
	Path     string
	Policy   config.AuditStoragePolicy
	Now      time.Time
	Owner    string
	LeaseTTL time.Duration
	Log      *slog.Logger
}

// Result describes committed work and its immutable plan.
type Result struct {
	RunID          string      `json:"run_id"`
	Plan           Plan        `json:"plan"`
	CompactPlan    CompactPlan `json:"compact_plan"`
	DetailGraphs   int64       `json:"detail_graphs"`
	SummaryGraphs  int64       `json:"summary_graphs"`
	ReclaimedBytes int64       `json:"reclaimed_bytes"`
	SizeState      SizeState   `json:"size_state"`
	Result         string      `json:"result"`
	ErrorClass     string      `json:"error_class,omitempty"`
	NextDueAt      *time.Time  `json:"next_due_at,omitempty"`
	Err            error       `json:"-"`
}

// Apply demotes and deletes eligible audit graphs in bounded transactions.
func Apply(ctx context.Context, options ApplyOptions) (result Result, returnErr error) {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if err := validateApplyOptions(options); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return unknownResult(), wrapError("start audit maintenance", err)
	}
	fileLock, err := acquireFileLock(options.Path)
	if errors.Is(err, ErrMaintenanceBusy) {
		return deferredResult(err), nil
	}
	if err != nil {
		return Result{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, fileLock.release()) }()

	database, err := openApplyDatabase(ctx, options.Path)
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := database.Close(); err != nil {
			returnErr = errors.Join(returnErr, wrapError("close audit maintenance database", err))
		}
	}()
	if err := auditstorage.MigrateNonblocking(ctx, database); err != nil {
		migrationErr := classifyMaintenanceWriteError(
			"migrate audit maintenance database",
			err,
		)
		if errors.Is(migrationErr, ErrMaintenanceBusy) {
			return deferredResult(migrationErr), nil
		}
		return Result{}, migrationErr
	}
	plan, err := initialRunPlan(options.Policy, options.Now)
	if err != nil {
		return Result{}, err
	}
	runID, err := newRunID()
	if err != nil {
		return Result{}, err
	}
	result = Result{
		RunID: runID, Plan: plan, CompactPlan: emptyCompactPlan(),
		DetailGraphs: 0, SummaryGraphs: 0, ReclaimedBytes: 0,
		SizeState: SizeStateUnknown, Result: "running", ErrorClass: "",
		NextDueAt: nil, Err: nil,
	}
	startedAt := options.Now.UTC()
	lease := maintenanceLease{owner: options.Owner, runID: runID, ttl: options.LeaseTTL}
	if err := acquireLease(ctx, database, lease, startedAt); err != nil {
		if errors.Is(err, ErrMaintenanceBusy) {
			return deferredResult(err), nil
		}
		return Result{}, err
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseLeaseBounded(ctx, database, lease))
	}()
	if err := validateApplyDatabase(ctx, database); err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}
	plan, err = previewApplyPlan(ctx, options)
	if err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}
	result.Plan = plan
	if err := lease.renew(ctx, database, maintenanceNow().UTC()); err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}
	if err := recordRunStartLogged(ctx, database, result, startedAt, options.Log); err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}

	detailGraphs, err := applyDetailBatches(ctx, database, lease, options)
	result.DetailGraphs = detailGraphs
	if err == nil {
		result.SummaryGraphs, err = applySummaryBatches(ctx, database, lease, options)
	}
	if err == nil {
		var sizeGraphs int64
		sizeGraphs, result.SizeState, err = applySizeBatches(
			ctx,
			database,
			lease,
			options,
		)
		result.SummaryGraphs += sizeGraphs
	}
	if err == nil {
		err = compactAfterApply(ctx, database, lease, options, &result)
	}
	return finishApply(ctx, database, options, result, err)
}

func unknownResult() Result {
	return Result{
		RunID: "", CompactPlan: emptyCompactPlan(),
		Plan: Plan{
			PlannedAt: time.Time{}, PolicyHash: "", DetailCutoff: nil,
			SummaryCutoff: time.Time{}, DetailCandidateGraphs: 0,
			SummaryCandidateGraphs: 0, ProtectedGraphs: 0, ProtectedBytes: 0,
			EstimatedDeleteBytes: 0,
		},
		DetailGraphs: 0, SummaryGraphs: 0, ReclaimedBytes: 0,
		SizeState: SizeStateUnknown, Result: "", ErrorClass: "",
		NextDueAt: nil, Err: nil,
	}
}

func previewApplyPlan(ctx context.Context, options ApplyOptions) (Plan, error) {
	snapshot, err := openDatabaseSnapshot(ctx, options.Path)
	if err != nil {
		return Plan{}, err
	}
	defer snapshot.cleanup()
	return previewDatabase(ctx, snapshot.database, options.Policy, options.Now)
}

func initialRunPlan(policy config.AuditStoragePolicy, now time.Time) (Plan, error) {
	policyHash, err := hashPolicy(policy)
	if err != nil {
		return Plan{}, err
	}
	detailCutoff := now.UTC().Add(-policy.FullDetailRetention)
	return Plan{
		PlannedAt: now.UTC(), PolicyHash: policyHash, DetailCutoff: &detailCutoff,
		SummaryCutoff:         now.UTC().Add(-policy.SummaryRetention),
		DetailCandidateGraphs: 0, SummaryCandidateGraphs: 0, ProtectedGraphs: 0,
		ProtectedBytes: 0, EstimatedDeleteBytes: 0,
	}, nil
}

func acquireLease(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	startedAt time.Time,
) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return classifyMaintenanceWriteError("begin audit maintenance run", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := lease.acquireTransaction(ctx, transaction, startedAt); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return classifyMaintenanceWriteError("commit audit maintenance lease", err)
	}
	return nil
}

func finishUnstartedApply(
	ctx context.Context,
	database *sql.DB,
	options ApplyOptions,
	result Result,
	applyErr error,
) (Result, error) {
	if errors.Is(applyErr, ErrMaintenanceBusy) {
		result.Result = "deferred"
		result.ErrorClass = "busy"
		result.Err = ErrMaintenanceBusy
	} else {
		result.Result = "failed"
		result.ErrorClass = classifyRunError(applyErr)
	}
	if err := recordTerminalRunBounded(ctx, database, result, maintenanceNow().UTC()); err != nil {
		return result, errors.Join(applyErr, err)
	}
	if result.Result == "deferred" {
		return result, nil
	}
	options.Log.WarnContext(ctx, "audit maintenance apply failed", "err", applyErr)
	return result, applyErr
}

func finishApply(
	ctx context.Context,
	database *sql.DB,
	options ApplyOptions,
	result Result,
	applyErr error,
) (Result, error) {
	completedAt := maintenanceNow().UTC()
	if errors.Is(applyErr, ErrMaintenanceBusy) {
		result.Result = "deferred"
		result.ErrorClass = "busy"
		result.Err = ErrMaintenanceBusy
		if recordErr := recordRunCompletionBounded(
			ctx, database, result, completedAt, nil,
		); recordErr != nil {
			return result, recordErr
		}
		return result, nil
	}
	if applyErr != nil {
		result.Result = "failed"
		result.ErrorClass = classifyRunError(applyErr)
		completionErr := recordRunCompletionBounded(
			ctx, database, result, completedAt, nil,
		)
		if completionErr != nil {
			return result, errors.Join(applyErr, completionErr)
		}
		options.Log.WarnContext(ctx, "audit maintenance apply failed", "err", applyErr)
		return result, applyErr
	}
	result.Result = "success"
	nextDueAt := completedAt.Add(options.Policy.MaintenanceInterval)
	result.NextDueAt = &nextDueAt
	if err := recordRunCompletionBounded(
		ctx, database, result, completedAt, &nextDueAt,
	); err != nil {
		return result, err
	}
	return result, nil
}

func validateApplyOptions(options ApplyOptions) error {
	if strings.TrimSpace(options.Path) == "" {
		return errors.New("audit database path is required")
	}
	if options.Now.IsZero() {
		return errors.New("audit maintenance clock is required")
	}
	if strings.TrimSpace(options.Owner) == "" {
		return errors.New("audit maintenance owner is required")
	}
	if options.LeaseTTL <= 0 {
		return errors.New("audit maintenance lease duration must be positive")
	}
	if options.Policy.MaintenanceBatchRows <= 0 {
		return errors.New("audit maintenance batch size must be positive")
	}
	if options.Policy.MaxSizeBytes < 0 {
		return errors.New("audit maintenance size target must not be negative")
	}
	if options.Policy.FullDetailRetention < 0 || options.Policy.SummaryRetention <= 0 {
		return errors.New("audit retention durations are invalid")
	}
	return nil
}

func openApplyDatabase(ctx context.Context, path string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Set("_busy_timeout", "0")
	query.Set("_foreign_keys", "1")
	uri.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, wrapError("open audit maintenance database", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, wrapError("connect audit maintenance database", err)
	}
	return database, nil
}

func validateApplyDatabase(ctx context.Context, database *sql.DB) error {
	var integrity string
	if err := database.QueryRowContext(ctx, `pragma quick_check(1)`).Scan(&integrity); err != nil {
		return wrapError("check audit database integrity", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("audit database integrity check failed: %s", integrity)
	}
	rows, err := database.QueryContext(ctx, `pragma foreign_key_check`)
	if err != nil {
		return wrapError("check audit foreign keys", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		return errors.New("audit database foreign key check failed")
	}
	if err := rows.Err(); err != nil {
		return wrapError("iterate audit foreign key check", err)
	}
	return nil
}

func applyDetailBatches(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	options ApplyOptions,
) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, wrapError("apply audit detail batches", err)
		}
		if err := lease.renew(ctx, database, maintenanceNow().UTC()); err != nil {
			return total, err
		}
		count, err := applyDetailBatch(ctx, database, options)
		if err != nil {
			return total, err
		}
		total += count
		if count == 0 {
			return total, nil
		}
	}
}

func applyDetailBatch(
	ctx context.Context,
	database *sql.DB,
	options ApplyOptions,
) (int64, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, classifyMaintenanceWriteError("begin audit detail batch", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := prepareSelectedGraphs(ctx, transaction); err != nil {
		return 0, err
	}
	detailCutoff := options.Now.Add(-options.Policy.FullDetailRetention)
	result, err := transaction.ExecContext(ctx, selectDetailGraphsSQL,
		detailCutoff.UTC().Format(time.RFC3339Nano),
		options.Policy.MaintenanceBatchRows,
	)
	if err != nil {
		return 0, classifyMaintenanceWriteError("select audit detail batch", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, wrapError("count audit detail batch", err)
	}
	if count == 0 {
		return 0, nil
	}
	if err := demoteSelectedDetail(ctx, transaction, options.Now); err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, classifyMaintenanceWriteError("commit audit detail batch", err)
	}
	return count, nil
}

func applySummaryBatches(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	options ApplyOptions,
) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, wrapError("apply audit summary batches", err)
		}
		if err := lease.renew(ctx, database, maintenanceNow().UTC()); err != nil {
			return total, err
		}
		count, err := applySummaryBatch(ctx, database, options)
		if err != nil {
			return total, err
		}
		total += count
		if count == 0 {
			return total, nil
		}
	}
}

func applySummaryBatch(
	ctx context.Context,
	database *sql.DB,
	options ApplyOptions,
) (int64, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, classifyMaintenanceWriteError("begin audit summary batch", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := prepareSelectedGraphs(ctx, transaction); err != nil {
		return 0, err
	}
	summaryCutoff := options.Now.Add(-options.Policy.SummaryRetention)
	result, err := transaction.ExecContext(ctx, selectSummaryGraphsSQL,
		summaryCutoff.UTC().Format(time.RFC3339Nano),
		options.Policy.MaintenanceBatchRows,
	)
	if err != nil {
		return 0, classifyMaintenanceWriteError("select audit summary batch", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, wrapError("count audit summary batch", err)
	}
	if count == 0 {
		return 0, nil
	}
	if err := deleteSelectedSummaries(ctx, transaction); err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, classifyMaintenanceWriteError("commit audit summary batch", err)
	}
	return count, nil
}

func applySizeBatches(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	options ApplyOptions,
) (int64, SizeState, error) {
	if options.Policy.MaxSizeBytes <= 0 {
		return 0, SizeStateDisabled, nil
	}
	var total int64
	candidateQueuePrepared := false
	for {
		if err := ctx.Err(); err != nil {
			return total, SizeStateUnknown, wrapError("apply audit size batches", err)
		}
		if err := lease.renew(ctx, database, maintenanceNow().UTC()); err != nil {
			return total, SizeStateUnknown, err
		}
		options.Log.DebugContext(ctx, logMeasureAuditSize)
		size, err := measureApplyDatabaseSize(ctx, database, options.Path)
		if err != nil {
			options.Log.DebugContext(ctx, logAuditSizeMeasurementFailed, "err", err)
			return total, SizeStateUnknown, err
		}
		if size.CompactedUsageBytes <= options.Policy.MaxSizeBytes {
			physicalBytes := size.DatabaseBytes + size.WALBytes
			if physicalBytes > options.Policy.MaxSizeBytes {
				return total, SizeStateReclaimPending, nil
			}
			return total, SizeStateWithinTarget, nil
		}
		if !candidateQueuePrepared {
			if err := prepareSizeCandidateQueue(ctx, database); err != nil {
				return total, SizeStateUnknown, err
			}
			candidateQueuePrepared = true
		}
		count, err := applyOldestSizeGraph(ctx, database)
		if err != nil {
			return total, SizeStateUnknown, err
		}
		if count == 0 {
			protectedGraphs, err := readCurrentProtectedGraphCount(ctx, database)
			if err != nil {
				return total, SizeStateUnknown, err
			}
			if protectedGraphs > 0 {
				return total, SizeStateConstrained, nil
			}
			return total, SizeStateOverTarget, nil
		}
		total += count
	}
}

func prepareSizeCandidateQueue(ctx context.Context, database *sql.DB) error {
	if _, err := database.ExecContext(ctx, prepareSizeCandidateQueueSQL); err != nil {
		return classifyMaintenanceWriteError("prepare audit size candidate queue", err)
	}
	return nil
}

func readCurrentProtectedGraphCount(ctx context.Context, database *sql.DB) (int64, error) {
	var count int64
	if err := database.QueryRowContext(ctx, `
		select count(*) from intake_events event
		where not (`+protectedGraphPredicate+`)
	`).Scan(&count); err != nil {
		return 0, wrapError("count protected audit size graphs", err)
	}
	return count, nil
}

func applyOldestSizeGraph(ctx context.Context, database *sql.DB) (int64, error) {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return 0, classifyMaintenanceWriteError("begin audit size batch", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := prepareSelectedGraphs(ctx, transaction); err != nil {
		return 0, err
	}
	result, err := transaction.ExecContext(ctx, selectOldestSizeGraphSQL)
	if err != nil {
		return 0, classifyMaintenanceWriteError("select audit size batch", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, wrapError("count audit size batch", err)
	}
	if count == 0 {
		return 0, nil
	}
	if _, err := transaction.ExecContext(ctx, `
		delete from maintenance_size_candidate_queue
		where event_id in (select event_id from maintenance_selected_event_ids)
	`); err != nil {
		return 0, classifyMaintenanceWriteError("advance audit size candidate queue", err)
	}
	if err := deleteSelectedSummaries(ctx, transaction); err != nil {
		return 0, err
	}
	if err := transaction.Commit(); err != nil {
		return 0, classifyMaintenanceWriteError("commit audit size batch", err)
	}
	return count, nil
}

func prepareSelectedGraphs(ctx context.Context, transaction *sql.Tx) error {
	if _, err := transaction.ExecContext(ctx, `
		create temp table if not exists maintenance_selected_event_ids (
			event_id text primary key
		);
		delete from maintenance_selected_event_ids;
	`); err != nil {
		return classifyMaintenanceWriteError("prepare audit maintenance batch", err)
	}
	return nil
}

const protectedGraphPredicate = `not exists (
	select 1 from intake_receipts related_receipt
	where related_receipt.event_id = event.event_id and (
		not exists (select 1 from gate_evaluations hot_evaluation
			where hot_evaluation.receipt_id = related_receipt.receipt_id
				and hot_evaluation.mode = 'hot')
		or exists (select 1 from intake_deferred deferred
			where deferred.receipt_id = related_receipt.receipt_id
				and deferred.state = 'pending')
		or exists (select 1 from deferred_audit_outbox outbox
			where outbox.receipt_id = related_receipt.receipt_id
				and outbox.state = 'pending')
		or exists (select 1 from deferred_audit_outbox_entries entry
			where entry.receipt_id = related_receipt.receipt_id
				and entry.delivered_at is null)
	)
)`

const selectDetailGraphsSQL = `
	insert into maintenance_selected_event_ids(event_id)
	select event.event_id
	from intake_events event
	where ` + protectedGraphPredicate + `
		and (select max(julianday(receipt.received_at))
			from intake_receipts receipt where receipt.event_id = event.event_id)
			< julianday(?)
		and (
			exists (select 1 from intake_event_details detail
				where detail.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_details detail
				join gate_evaluations evaluation using(evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_layer_details detail
				join gate_evaluations evaluation using(evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from gate_evaluation_label_details detail
				join gate_evaluations evaluation using(evaluation_id)
				where evaluation.event_id = event.event_id)
			or exists (select 1 from deferred_audit_outbox_entry_details detail
				join deferred_audit_outbox outbox using(receipt_id)
				where outbox.event_id = event.event_id)
		)
	order by (select max(julianday(receipt.received_at))
		from intake_receipts receipt where receipt.event_id = event.event_id), event.event_id
	limit ?`

const selectSummaryGraphsSQL = `
	insert into maintenance_selected_event_ids(event_id)
	select event.event_id
	from intake_events event
	where ` + protectedGraphPredicate + `
		and (select max(julianday(receipt.received_at))
			from intake_receipts receipt where receipt.event_id = event.event_id)
			< julianday(?)
	order by (select max(julianday(receipt.received_at))
		from intake_receipts receipt where receipt.event_id = event.event_id), event.event_id
	limit ?`

const prepareSizeCandidateQueueSQL = `
	drop table if exists temp.maintenance_size_candidate_queue;
	create temp table maintenance_size_candidate_queue (
		sequence integer primary key autoincrement,
		event_id text not null unique
	);
	insert into maintenance_size_candidate_queue(event_id)
	select event.event_id
	from intake_events event
	where ` + protectedGraphPredicate + `
	order by (select max(julianday(receipt.received_at))
		from intake_receipts receipt where receipt.event_id = event.event_id), event.event_id;
`

const selectOldestSizeGraphSQL = `
	insert into maintenance_selected_event_ids(event_id)
	select event.event_id
	from maintenance_size_candidate_queue candidate
	join intake_events event on event.event_id = candidate.event_id
	where ` + protectedGraphPredicate + `
	order by candidate.sequence
	limit 1`

func demoteSelectedDetail(
	ctx context.Context,
	transaction *sql.Tx,
	now time.Time,
) error {
	changedAt := now.UTC().Format(time.RFC3339Nano)
	statements := []string{
		`delete from intake_event_details where event_id in
			(select event_id from maintenance_selected_event_ids)`,
		`delete from gate_evaluation_label_details where evaluation_id in
			(select evaluation_id from gate_evaluations where event_id in
				(select event_id from maintenance_selected_event_ids))`,
		`delete from gate_evaluation_layer_details where evaluation_id in
			(select evaluation_id from gate_evaluations where event_id in
				(select event_id from maintenance_selected_event_ids))`,
		`delete from gate_evaluation_details where evaluation_id in
			(select evaluation_id from gate_evaluations where event_id in
				(select event_id from maintenance_selected_event_ids))`,
		`delete from deferred_audit_outbox_entry_details where receipt_id in
			(select receipt_id from deferred_audit_outbox where event_id in
				(select event_id from maintenance_selected_event_ids))`,
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return classifyMaintenanceWriteError("demote audit detail batch", err)
		}
	}
	if _, err := transaction.ExecContext(ctx, `update intake_event_detail_manifest
		set available_classes_json = '[]', state = case
			when recorded_classes_json = '[]' then 'not_recorded' else 'expired' end,
			state_changed_at = ?
		where event_id in (select event_id from maintenance_selected_event_ids)`, changedAt); err != nil {
		return classifyMaintenanceWriteError("demote audit detail batch", err)
	}
	if _, err := transaction.ExecContext(ctx, `update gate_evaluations
		set detail_state = case
			when detail_state = 'not_recorded' then 'not_recorded' else 'expired' end
		where event_id in (select event_id from maintenance_selected_event_ids)`); err != nil {
		return classifyMaintenanceWriteError("demote audit detail batch", err)
	}
	if _, err := transaction.ExecContext(ctx, `update deferred_audit_outbox_entries
		set payload_available = 0, payload_state_changed_at = ?
		where payload_recorded = 1 and receipt_id in
			(select receipt_id from deferred_audit_outbox where event_id in
				(select event_id from maintenance_selected_event_ids))`, changedAt); err != nil {
		return classifyMaintenanceWriteError("demote audit detail batch", err)
	}
	return nil
}

func deleteSelectedSummaries(ctx context.Context, transaction *sql.Tx) error {
	statements := []string{
		`create temp table if not exists maintenance_selected_receipt_ids
			(receipt_id integer primary key)`,
		`delete from maintenance_selected_receipt_ids`,
		`insert into maintenance_selected_receipt_ids
			select receipt_id from intake_receipts where event_id in
				(select event_id from maintenance_selected_event_ids)`,
		`create temp table if not exists maintenance_selected_evaluation_ids
			(evaluation_id text primary key)`,
		`delete from maintenance_selected_evaluation_ids`,
		`insert into maintenance_selected_evaluation_ids
			select evaluation_id from gate_evaluations where event_id in
				(select event_id from maintenance_selected_event_ids)`,
		`create temp table if not exists maintenance_selected_audit_event_ids
			(event_id text primary key)`,
		`delete from maintenance_selected_audit_event_ids`,
		`insert into maintenance_selected_audit_event_ids
			select distinct selected.audit_event_id
			from deferred_audit_outbox_entries selected
			where selected.receipt_id in (select receipt_id from maintenance_selected_receipt_ids)
				and not exists (
					select 1 from deferred_audit_outbox_entries retained
					where retained.audit_event_id = selected.audit_event_id
						and retained.receipt_id not in
							(select receipt_id from maintenance_selected_receipt_ids)
				)`,
		`delete from violations where event_id in
			(select event_id from maintenance_selected_audit_event_ids)`,
		`delete from decisions where event_id in
			(select event_id from maintenance_selected_audit_event_ids)`,
		`delete from operations where event_id in
			(select event_id from maintenance_selected_audit_event_ids)`,
		`delete from events where event_id in
			(select event_id from maintenance_selected_audit_event_ids)`,
		`delete from deferred_audit_outbox_entry_details where receipt_id in
			(select receipt_id from maintenance_selected_receipt_ids)`,
		`delete from deferred_audit_outbox_entries where receipt_id in
			(select receipt_id from maintenance_selected_receipt_ids)`,
		`delete from deferred_audit_outbox where receipt_id in
			(select receipt_id from maintenance_selected_receipt_ids)`,
		`delete from gate_evaluation_label_details where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from gate_evaluation_labels where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from gate_evaluation_layer_details where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from gate_evaluation_layers where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from gate_evaluation_details where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from gate_evaluations where evaluation_id in
			(select evaluation_id from maintenance_selected_evaluation_ids)`,
		`delete from intake_deferred where receipt_id in
			(select receipt_id from maintenance_selected_receipt_ids)`,
		`delete from intake_event_details where event_id in
			(select event_id from maintenance_selected_event_ids)`,
		`delete from intake_event_detail_manifest where event_id in
			(select event_id from maintenance_selected_event_ids)`,
		`delete from intake_receipts where receipt_id in
			(select receipt_id from maintenance_selected_receipt_ids)`,
		`delete from intake_events where event_id in
			(select event_id from maintenance_selected_event_ids)`,
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement); err != nil {
			return classifyMaintenanceWriteError("delete audit summary batch", err)
		}
	}
	return nil
}

func recordRunStart(
	ctx context.Context,
	database *sql.DB,
	result Result,
	startedAt time.Time,
) error {
	planJSON, err := json.Marshal(result.Plan)
	if err != nil {
		return wrapError("encode audit maintenance plan", err)
	}
	_, err = database.ExecContext(ctx, `
		insert into audit_maintenance_runs (
			run_id, planned_at, started_at, policy_hash, plan_json, result
		) values (?, ?, ?, ?, ?, 'running')
	`, result.RunID, result.Plan.PlannedAt.Format(time.RFC3339Nano),
		startedAt.Format(time.RFC3339Nano), result.Plan.PolicyHash, string(planJSON))
	if err != nil {
		return classifyMaintenanceWriteError("record audit maintenance start", err)
	}
	return nil
}

func recordRunStartLogged(
	ctx context.Context,
	database *sql.DB,
	result Result,
	startedAt time.Time,
	log *slog.Logger,
) error {
	log.DebugContext(ctx, logRecordMaintenanceStart)
	err := recordRunStart(ctx, database, result, startedAt)
	if err != nil {
		log.DebugContext(ctx, logMaintenanceStartRecordFailed, "err", err)
	}
	return err
}

func recordTerminalRunBounded(
	ctx context.Context,
	database *sql.DB,
	result Result,
	completedAt time.Time,
) error {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), leaseCleanupTimeout,
	)
	defer cancel()
	for {
		err := recordTerminalRun(cleanupContext, database, result, completedAt)
		if !errors.Is(err, ErrMaintenanceBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-cleanupContext.Done():
			timer.Stop()
			return wrapError("record terminal audit maintenance run", cleanupContext.Err())
		case <-timer.C:
		}
	}
}

func recordTerminalRun(
	ctx context.Context,
	database *sql.DB,
	result Result,
	completedAt time.Time,
) error {
	planJSON, err := json.Marshal(result.Plan)
	if err != nil {
		return wrapError("encode audit maintenance plan", err)
	}
	_, err = database.ExecContext(ctx, `
		insert into audit_maintenance_runs (
			run_id, planned_at, started_at, completed_at, policy_hash, plan_json,
			detail_graphs, summary_graphs, reclaimed_bytes, result, error_class
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.RunID, result.Plan.PlannedAt.Format(time.RFC3339Nano),
		result.Plan.PlannedAt.Format(time.RFC3339Nano),
		completedAt.UTC().Format(time.RFC3339Nano), result.Plan.PolicyHash,
		string(planJSON), result.DetailGraphs, result.SummaryGraphs, result.ReclaimedBytes,
		result.Result, result.ErrorClass)
	if err != nil {
		return classifyMaintenanceWriteError("record terminal audit maintenance run", err)
	}
	return nil
}

func recordRunCompletion(
	ctx context.Context,
	database *sql.DB,
	result Result,
	completedAt time.Time,
	nextDueAt *time.Time,
) error {
	var nextDue sql.NullString
	if nextDueAt != nil {
		nextDue = sql.NullString{
			String: nextDueAt.UTC().Format(time.RFC3339Nano), Valid: true,
		}
	}
	_, err := database.ExecContext(ctx, `
		update audit_maintenance_runs set completed_at = ?, detail_graphs = ?,
			summary_graphs = ?, reclaimed_bytes = ?, result = ?, error_class = ?,
			next_due_at = ?
		where run_id = ?
	`, completedAt.UTC().Format(time.RFC3339Nano), result.DetailGraphs,
		result.SummaryGraphs, result.ReclaimedBytes, result.Result, result.ErrorClass,
		nextDue, result.RunID)
	if err != nil {
		return classifyMaintenanceWriteError("record audit maintenance completion", err)
	}
	return nil
}

func recordRunCompletionBounded(
	ctx context.Context,
	database *sql.DB,
	result Result,
	completedAt time.Time,
	nextDueAt *time.Time,
) error {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), leaseCleanupTimeout,
	)
	defer cancel()
	return recordRunCompletionWithRetry(
		cleanupContext, database, result, completedAt, nextDueAt,
	)
}

func recordRunCompletionWithRetry(
	ctx context.Context,
	database *sql.DB,
	result Result,
	completedAt time.Time,
	nextDueAt *time.Time,
) error {
	for {
		err := recordRunCompletion(ctx, database, result, completedAt, nextDueAt)
		if !errors.Is(err, ErrMaintenanceBusy) {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return wrapError("record audit maintenance completion", ctx.Err())
		case <-timer.C:
		}
	}
}

func classifyMaintenanceWriteError(action string, err error) error {
	var sqliteError sqlite3.Error
	if errors.As(err, &sqliteError) &&
		(sqliteError.Code == sqlite3.ErrBusy || sqliteError.Code == sqlite3.ErrLocked) {
		return ErrMaintenanceBusy
	}
	return wrapError(action, err)
}

func classifyRunError(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	if errors.Is(err, ErrLeaseLost) {
		return "lease_lost"
	}
	return "apply"
}

func deferredResult(err error) Result {
	return Result{
		RunID: "", CompactPlan: emptyCompactPlan(),
		Plan: Plan{
			PlannedAt: time.Time{}, PolicyHash: "", DetailCutoff: nil,
			SummaryCutoff: time.Time{}, DetailCandidateGraphs: 0,
			SummaryCandidateGraphs: 0, ProtectedGraphs: 0, ProtectedBytes: 0,
			EstimatedDeleteBytes: 0,
		},
		DetailGraphs: 0, SummaryGraphs: 0, ReclaimedBytes: 0,
		SizeState: SizeStateUnknown, Result: "deferred", ErrorClass: "busy",
		NextDueAt: nil, Err: err,
	}
}

func newRunID() (string, error) {
	randomBytes := make([]byte, runIDBytes)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", wrapError("create audit maintenance run id", err)
	}
	return hex.EncodeToString(randomBytes), nil
}
