package auditmaintenance_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/intake"
)

func TestApplyMatchesPreviewAtSameClockAndPolicy(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	plan, err := auditmaintenance.Preview(t.Context(), fixture.path, fixture.policy, fixture.now)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(result.Plan, plan) {
		t.Fatalf("apply plan = %#v, want %#v", result.Plan, plan)
	}
	if result.DetailGraphs != plan.DetailCandidateGraphs {
		t.Fatalf("detail graphs = %d, want %d", result.DetailGraphs, plan.DetailCandidateGraphs)
	}
	if result.SummaryGraphs != plan.SummaryCandidateGraphs {
		t.Fatalf("summary graphs = %d, want %d", result.SummaryGraphs, plan.SummaryCandidateGraphs)
	}
}

func TestApplyDemotesDetailBeforeDeletingSummary(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "old-summary", 0)
	assertGraphCount(t, database, "old-detail", 1)
	var state string
	var details int
	if err := database.QueryRowContext(t.Context(), `
		select manifest.state,
			(select count(*) from intake_event_details detail
			where detail.event_id = manifest.event_id)
		from intake_event_detail_manifest manifest where event_id = 'old-detail'
	`).Scan(&state, &details); err != nil {
		t.Fatalf("read demoted detail: %v", err)
	}
	if state != "expired" || details != 0 {
		t.Fatalf("demoted detail = state %q rows %d, want expired and 0", state, details)
	}
}

func TestApplyNeverDeletesProtectedGraph(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	for _, eventID := range []string{
		"awaiting-hot", "pending-deferred", "expired-evaluation-claim",
		"pending-audit", "expired-audit-claim", "partly-delivered",
	} {
		assertGraphCount(t, database, eventID, 1)
	}
	assertForeignKeys(t, database)
}

func TestApplyIsIdempotent(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if result.DetailGraphs != 0 || result.SummaryGraphs != 0 {
		t.Fatalf("second apply counts = %d/%d, want 0/0", result.DetailGraphs, result.SummaryGraphs)
	}
}

func TestApplyRollsBackOnlyFailingBatch(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	store, err := intake.OpenSQLite(t.Context(), fixture.path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	insertFixtureGraph(
		t, store, store.Handle(), "older-committed", fixture.now.Add(-50*24*time.Hour), true,
	)
	if _, err := store.Handle().ExecContext(t.Context(), `
		create trigger fail_old_summary before delete on intake_events
		when old.event_id = 'old-summary'
		begin
			select raise(abort, 'forced summary batch failure');
		end
	`); err != nil {
		_ = store.Close()
		t.Fatalf("create failure trigger: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fixture.policy.MaintenanceBatchRows = 1
	_, err = auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err == nil || !strings.Contains(err.Error(), "forced summary batch failure") {
		t.Fatalf("Apply error = %v, want forced batch failure", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "older-committed", 0)
	assertGraphCount(t, database, "old-summary", 1)
	assertForeignKeys(t, database)
	var leases int
	if err := database.QueryRow(`select count(*) from audit_maintenance_lease`).Scan(&leases); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leases != 0 {
		t.Fatalf("lease rows after failure = %d, want 0", leases)
	}
}

func TestApplyStopsOnBusyWithoutBreakingConcurrentAppend(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	blocker, err := sql.Open("sqlite3", fixture.path)
	if err != nil {
		t.Fatalf("open blocker: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	connection, err := blocker.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve blocker connection: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(t.Context(), `begin immediate`); err != nil {
		t.Fatalf("begin competing write: %v", err)
	}
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Result != "deferred" || !errors.Is(result.Err, auditmaintenance.ErrMaintenanceBusy) {
		t.Fatalf("result = %#v, want deferred busy", result)
	}
	if _, err := connection.ExecContext(t.Context(), `commit`); err != nil {
		t.Fatalf("commit competing write: %v", err)
	}
	store, err := intake.OpenSQLite(t.Context(), fixture.path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite after deferred maintenance: %v", err)
	}
	defer func() { _ = store.Close() }()
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "append-after-busy", RecordedAt: fixture.now, System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil || receipt.ReceiptID == 0 {
		t.Fatalf("Append after deferred maintenance = %#v, %v", receipt, err)
	}
}

func TestApplyDefersWhenRunRecordMeetsConcurrentWriter(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	insertProtectedPreviewGraphs(t, fixture, database)

	type applyOutcome struct {
		result auditmaintenance.Result
		err    error
	}
	outcome := make(chan applyOutcome, 1)
	go func() {
		result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
		outcome <- applyOutcome{result: result, err: err}
	}()

	waitForMaintenanceLease(t, database)
	connection, err := database.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve concurrent writer: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(t.Context(), `begin immediate`); err != nil {
		t.Fatalf("begin concurrent writer: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, err := connection.ExecContext(t.Context(), `commit`); err != nil {
		t.Fatalf("commit concurrent writer: %v", err)
	}

	completed := <-outcome
	if completed.err != nil {
		t.Fatalf("Apply: %v", completed.err)
	}
	if completed.result.Result != "deferred" ||
		!errors.Is(completed.result.Err, auditmaintenance.ErrMaintenanceBusy) {
		t.Fatalf("result = %#v, want deferred busy", completed.result)
	}
	assertLastRunResult(t, database, "deferred", "busy")
	assertRecordedPlan(t, database, completed.result.RunID, completed.result.Plan)
}

func TestApplyReleasesLeaseAndFileLockAfterCancellation(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	insertProtectedPreviewGraphs(t, fixture, database)
	ctx, cancel := context.WithCancel(t.Context())
	completed := make(chan error, 1)
	go func() {
		_, err := auditmaintenance.Apply(ctx, applyOptions(fixture))
		completed <- err
	}()

	waitForMaintenanceLease(t, database)
	cancel()
	if err := <-completed; !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want context canceled", err)
	}
	var leases int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from audit_maintenance_lease`,
	).Scan(&leases); err != nil {
		t.Fatalf("count maintenance leases: %v", err)
	}
	if leases != 0 {
		t.Fatalf("maintenance leases = %d, want 0", leases)
	}
	assertLastRunResult(t, database, "failed", "cancelled")
	retry, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply after cancellation: %v", err)
	}
	if retry.Result != "success" {
		t.Fatalf("result after cancellation = %q, want success", retry.Result)
	}
}

func TestApplyRecordsForeignKeyValidationFailure(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(t.Context(), `pragma foreign_keys = off`); err != nil {
		t.Fatalf("disable foreign keys: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		) values (
			'broken-foreign-key', 999999, 'missing-event', 1, 'hot', 'config',
			'version', 'commit', 'build', 'input', ?, ?, 'allow',
			'deterministic', 'allow', 0, 1, 0, 0, 'available'
		)
	`, fixture.now.Format(time.RFC3339Nano), fixture.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert foreign key violation: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `pragma foreign_keys = on`); err != nil {
		t.Fatalf("restore foreign keys: %v", err)
	}

	_, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err == nil || !strings.Contains(err.Error(), "foreign key check failed") {
		t.Fatalf("Apply error = %v, want foreign key failure", err)
	}
	assertLastRunResult(t, database, "failed", "apply")
	assertGraphCount(t, database, "old-summary", 1)
}

func assertLastRunResult(t *testing.T, database *sql.DB, result string, errorClass string) {
	t.Helper()
	var recordedResult string
	var recordedErrorClass string
	if err := database.QueryRowContext(t.Context(), `
		select result, error_class from audit_maintenance_runs
		order by rowid desc limit 1
	`).Scan(&recordedResult, &recordedErrorClass); err != nil {
		t.Fatalf("read last maintenance run: %v", err)
	}
	if recordedResult != result || recordedErrorClass != errorClass {
		t.Fatalf(
			"last maintenance run = %q/%q, want %q/%q",
			recordedResult,
			recordedErrorClass,
			result,
			errorClass,
		)
	}
}

func assertRecordedPlan(
	t *testing.T,
	database *sql.DB,
	runID string,
	want auditmaintenance.Plan,
) {
	t.Helper()
	var planJSON string
	if err := database.QueryRowContext(t.Context(), `
		select plan_json from audit_maintenance_runs where run_id = ?
	`, runID).Scan(&planJSON); err != nil {
		t.Fatalf("read recorded maintenance plan: %v", err)
	}
	var recorded auditmaintenance.Plan
	if err := json.Unmarshal([]byte(planJSON), &recorded); err != nil {
		t.Fatalf("decode recorded maintenance plan: %v", err)
	}
	if !reflect.DeepEqual(recorded, want) {
		t.Fatalf("recorded maintenance plan = %#v, want %#v", recorded, want)
	}
}

func insertProtectedPreviewGraphs(
	t *testing.T,
	fixture maintenanceFixture,
	database *sql.DB,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
		with recursive sequence(value) as (
			select 1 union all select value + 1 from sequence where value < 5000
		)
		insert into intake_events (
			event_id, schema_version, recorded_at, system, session_id, turn_id,
			event_name, tool_name, tool_use_id, cwd, effective_cwd, command,
			file_path, raw_payload_hash
		)
		select printf('busy-%05d', value), 1, ?, 'codex', 'session', '',
			'PreToolUse', 'exec_command', '', '', '', '', '', ''
		from sequence
	`, fixture.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert busy events: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into intake_receipts(event_id, received_at)
		select event_id, ? from intake_events where event_id like 'busy-%'
	`, fixture.now.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert busy receipts: %v", err)
	}
}

func waitForMaintenanceLease(t *testing.T, database *sql.DB) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var leases int
		if err := database.QueryRowContext(
			t.Context(), `select count(*) from audit_maintenance_lease`,
		).Scan(&leases); err != nil {
			t.Fatalf("observe maintenance lease: %v", err)
		}
		if leases == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("maintenance lease was not acquired within five seconds")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestApplyCannotDeleteConcurrentHotEvaluationReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	insertFixtureGraph(t, store, store.Handle(), "concurrent-target", now.Add(-40*24*time.Hour), true)
	for i := range 30 {
		insertFixtureGraph(
			t, store, store.Handle(), fmt.Sprintf("old-%02d", i),
			now.Add(time.Duration(-50+i)*24*time.Hour), true,
		)
	}
	fixture := maintenanceFixture{path: path, policy: balancedMaintenancePolicy(), now: now}
	fixture.policy.MaintenanceBatchRows = 1
	start := make(chan struct{})
	appendResult := make(chan intake.AppendResult, 1)
	appendError := make(chan error, 1)
	go func() {
		<-start
		result, appendErr := store.Append(t.Context(), intake.Record{
			EventID: "concurrent-target", RecordedAt: now, System: "codex",
			SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
			RawPayload: []byte(`{"new":true}`), NormalizedJSON: json.RawMessage(`{"new":true}`),
		})
		appendResult <- result
		appendError <- appendErr
	}()
	close(start)
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	appended := <-appendResult
	if err := <-appendError; err != nil {
		t.Fatalf("concurrent Append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	database := openReadWriteDatabase(t, path)
	var count int
	if err := database.QueryRowContext(t.Context(), `
		select count(*) from intake_receipts
		where receipt_id = ? and event_id = 'concurrent-target'
	`, appended.ReceiptID).Scan(&count); err != nil {
		t.Fatalf("query concurrent receipt: %v", err)
	}
	if count != 1 {
		t.Fatalf("concurrent receipt rows = %d, want 1", count)
	}
	assertForeignKeys(t, database)
}

func TestApplyOneRowBatchesPreserveConcurrentAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range 20 {
		insertFixtureGraph(
			t, store, store.Handle(), fmt.Sprintf("eligible-%02d", i),
			now.Add(time.Duration(-50-i)*24*time.Hour), true,
		)
	}
	fixture := maintenanceFixture{path: path, policy: balancedMaintenancePolicy(), now: now}
	fixture.policy.MaintenanceBatchRows = 1
	var waitGroup sync.WaitGroup
	writtenReceipts := make(chan int64, 40)
	writeErrors := make(chan error, 1)
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for i := range 40 {
			result, appendErr := store.Append(t.Context(), intake.Record{
				EventID: fmt.Sprintf("live-%02d", i), RecordedAt: now, System: "codex",
				SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
				RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
			})
			if appendErr != nil {
				writeErrors <- appendErr
				return
			}
			writtenReceipts <- result.ReceiptID
		}
	}()
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	waitGroup.Wait()
	close(writtenReceipts)
	select {
	case err := <-writeErrors:
		t.Fatalf("concurrent append loop: %v", err)
	default:
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	database := openReadWriteDatabase(t, path)
	for receiptID := range writtenReceipts {
		var count int
		if err := database.QueryRow(
			`select count(*) from intake_receipts where receipt_id = ?`, receiptID,
		).Scan(&count); err != nil {
			t.Fatalf("query receipt %d: %v", receiptID, err)
		}
		if count != 1 {
			t.Fatalf("receipt %d rows = %d, want 1", receiptID, count)
		}
	}
	assertForeignKeys(t, database)
}

func TestApplyRejectsOverlappingLease(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(t.Context(), `
		insert into audit_maintenance_lease(singleton, owner, run_id, expires_at)
		values (1, 'other', 'other-run', ?)
	`, fixture.now.Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert competing lease: %v", err)
	}
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !errors.Is(result.Err, auditmaintenance.ErrMaintenanceBusy) || result.Result != "deferred" {
		t.Fatalf("result = %#v, want deferred maintenance busy", result)
	}
	assertGraphCount(t, database, "old-summary", 1)
}

func TestApplyReleasesOwnedLeaseAfterSuccess(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	var count int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from audit_maintenance_lease`,
	).Scan(&count); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if count != 0 {
		t.Fatalf("lease rows = %d, want 0", count)
	}
}

func TestApplyKeepsSharedAuditEventReferencedByYoungerGraph(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	database := store.Handle()
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	oldReceipt := insertCompleteOutboxGraph(t, store, database, "old-shared", now.Add(-40*24*time.Hour))
	youngReceipt := insertCompleteOutboxGraph(t, store, database, "young-shared", now.Add(-time.Hour))
	for _, receiptID := range []int64{oldReceipt, youngReceipt} {
		if _, err := database.ExecContext(t.Context(), `
			update deferred_audit_outbox_entries set audit_event_id = 'shared-audit'
			where receipt_id = ?
		`, receiptID); err != nil {
			t.Fatalf("share audit event: %v", err)
		}
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into events(event_id, message) values ('shared-audit', 'shared')
	`); err != nil {
		t.Fatalf("insert shared audit event: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fixture := maintenanceFixture{path: path, policy: balancedMaintenancePolicy(), now: now}
	if _, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	database = openReadWriteDatabase(t, path)
	assertGraphCount(t, database, "old-shared", 0)
	assertGraphCount(t, database, "young-shared", 1)
	var count int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from events where event_id = 'shared-audit'`,
	).Scan(&count); err != nil {
		t.Fatalf("count shared audit event: %v", err)
	}
	if count != 1 {
		t.Fatalf("shared audit event rows = %d, want 1", count)
	}
}

func applyOptions(fixture maintenanceFixture) auditmaintenance.ApplyOptions {
	return auditmaintenance.ApplyOptions{
		Path: fixture.path, Policy: fixture.policy, Now: fixture.now,
		Owner: "test-owner", LeaseTTL: time.Minute,
	}
}

func assertGraphCount(t *testing.T, database *sql.DB, eventID string, want int) {
	t.Helper()
	var count int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from intake_events where event_id = ?`, eventID,
	).Scan(&count); err != nil {
		t.Fatalf("count graph %s: %v", eventID, err)
	}
	if count != want {
		t.Fatalf("graph %s count = %d, want %d", eventID, count, want)
	}
}

func assertForeignKeys(t *testing.T, database *sql.DB) {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), `pragma foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign key check returned a violation")
	}
}

func insertCompleteOutboxGraph(
	t *testing.T,
	store *intake.Store,
	database *sql.DB,
	eventID string,
	receivedAt time.Time,
) int64 {
	t.Helper()
	receipt := insertFixtureGraph(t, store, database, eventID, receivedAt, true)
	evaluationID := "evaluation-" + eventID
	if _, err := database.ExecContext(t.Context(), `
		insert into deferred_audit_outbox (
			receipt_id, event_id, evaluation_id, state, created_at, completed_at,
			claim_attempt
		) values (?, ?, ?, 'complete', ?, ?, 1)
	`, receipt.ReceiptID, eventID, evaluationID,
		receivedAt.Format(time.RFC3339Nano), receivedAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert complete outbox: %v", err)
	}
	insertOutboxEntry(t, database, receipt.ReceiptID, 0, eventID+"-audit", true)
	return receipt.ReceiptID
}

func TestApplyCancellationDoesNotDeleteProtectedGraph(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := auditmaintenance.Apply(ctx, applyOptions(fixture))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Apply error = %v, want context canceled", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "old-summary", 1)
}

func TestApplyRecordsImmutablePlanAndResult(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	var planJSON string
	var recordedResult string
	if err := database.QueryRowContext(t.Context(), `
		select plan_json, result from audit_maintenance_runs where run_id = ?
	`, result.RunID).Scan(&planJSON, &recordedResult); err != nil {
		t.Fatalf("read maintenance run: %v", err)
	}
	var plan auditmaintenance.Plan
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		t.Fatalf("decode plan: %v", err)
	}
	if !reflect.DeepEqual(plan, result.Plan) || recordedResult != "success" {
		t.Fatalf("recorded plan/result = %#v/%q, want %#v/success", plan, recordedResult, result.Plan)
	}
}
