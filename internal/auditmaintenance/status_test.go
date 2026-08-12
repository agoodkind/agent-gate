package auditmaintenance_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/intake"
)

func TestReadStatusReportsDatabaseState(t *testing.T) {
	fixture := createMaintenanceFixture(t)

	status, err := auditmaintenance.ReadStatus(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if !reflect.DeepEqual(status.Policy, fixture.policy) {
		t.Fatalf("policy = %#v, want %#v", status.Policy, fixture.policy)
	}
	if status.DatabaseBytes <= 0 {
		t.Fatalf("database bytes = %d, want positive", status.DatabaseBytes)
	}
	if status.OldestDetailAt == nil || status.OldestSummaryAt == nil {
		t.Fatalf(
			"oldest detail/summary = %v/%v, want both timestamps",
			status.OldestDetailAt,
			status.OldestSummaryAt,
		)
	}
	if status.ProtectedGraphs != 6 {
		t.Fatalf("protected graphs = %d, want 6", status.ProtectedGraphs)
	}
	if !status.IntegrityOK || status.IntegrityError != "" {
		t.Fatalf("integrity = %t %q, want healthy", status.IntegrityOK, status.IntegrityError)
	}
	if status.FullCompactNeeded {
		t.Fatal("full compact needed = true for incremental auto-vacuum database")
	}
	if status.MaintenanceDueAt == nil || !status.Overdue {
		t.Fatalf(
			"maintenance due/overdue = %v/%t, want overdue migration-derived deadline",
			status.MaintenanceDueAt,
			status.Overdue,
		)
	}
	if status.LastRun != nil || status.NextAttemptAt != nil {
		t.Fatalf("missing metadata became last run/next attempt: %#v", status)
	}
}

func TestReadStatusOrdersGraphTimesByInstant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	database := store.Handle()
	laterLexicalFirst := insertFixtureGraph(
		t, store, database, "later-lexical-first", time.Now().UTC(), true,
	)
	earlierLexicalLast := insertFixtureGraph(
		t, store, database, "earlier-lexical-last", time.Now().UTC(), true,
	)
	if _, err := database.ExecContext(t.Context(), `
		update intake_receipts set received_at = ? where receipt_id = ?;
		update intake_receipts set received_at = ? where receipt_id = ?;
	`,
		"2030-08-25T01:00:00-10:00", laterLexicalFirst.ReceiptID,
		"2030-08-25T10:30:00+00:00", earlierLexicalLast.ReceiptID,
	); err != nil {
		_ = store.Close()
		t.Fatalf("set offset receipt times: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close offset status fixture: %v", err)
	}

	status, err := auditmaintenance.ReadStatus(
		t.Context(), path, balancedMaintenancePolicy(), time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	want := time.Date(2030, 8, 25, 10, 30, 0, 0, time.UTC)
	if status.OldestDetailAt == nil || !status.OldestDetailAt.Equal(want) {
		t.Fatalf("oldest detail = %v, want %s", status.OldestDetailAt, want)
	}
	if status.OldestSummaryAt == nil || !status.OldestSummaryAt.Equal(want) {
		t.Fatalf("oldest summary = %v, want %s", status.OldestSummaryAt, want)
	}
}

func TestReadStatusUsesLastSuccessfulCompletionForDueTime(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(t.Context(), `
		delete from audit_maintenance_runs;
		insert into audit_maintenance_runs (
			run_id, planned_at, started_at, completed_at, policy_hash, plan_json,
			detail_graphs, summary_graphs, reclaimed_bytes, result, error_class,
			next_due_at
		) values
			('success', '2030-08-28T09:00:00Z', '2030-08-28T09:00:01Z',
			 '2030-08-28T09:00:02Z', 'policy', '{}', 1, 0, 0, 'success', '', null),
			('failed', '2030-08-30T09:00:00Z', '2030-08-30T09:00:01Z',
			 '2030-08-30T09:00:02Z', 'policy', '{}', 0, 0, 0, 'failed', 'busy', null)
	`); err != nil {
		t.Fatalf("insert maintenance runs: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close maintenance database: %v", err)
	}

	status, err := auditmaintenance.ReadStatus(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	wantDueAt := time.Date(2030, 8, 29, 9, 0, 2, 0, time.UTC)
	if status.MaintenanceDueAt == nil || !status.MaintenanceDueAt.Equal(wantDueAt) {
		t.Fatalf("maintenance due at = %v, want %s", status.MaintenanceDueAt, wantDueAt)
	}
	if status.LastRun == nil || status.LastRun.RunID != "failed" {
		t.Fatalf("last run = %#v, want newest failed run", status.LastRun)
	}
}

func TestReadStatusDoesNotWriteDatabase(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	before := snapshotSQLiteFiles(t, fixture.path)

	if _, err := auditmaintenance.ReadStatus(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	); err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}

	after := snapshotSQLiteFiles(t, fixture.path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite files changed during status\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestReadStatusIgnoresCheckpointedWALAllocation(t *testing.T) {
	path, store := createActiveWALFixture(t)
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close active WAL store: %v", err)
		}
	}()
	database := store.Handle()
	if _, err := database.ExecContext(t.Context(), `
		create table wal_padding (value blob not null);
		insert into wal_padding values (zeroblob(1048576));
	`); err != nil {
		t.Fatalf("write WAL padding: %v", err)
	}
	var busy int
	var frames int64
	var checkpointed int64
	if err := database.QueryRowContext(t.Context(), `pragma wal_checkpoint(passive)`).Scan(
		&busy,
		&frames,
		&checkpointed,
	); err != nil {
		t.Fatalf("passive checkpoint: %v", err)
	}
	if busy != 0 || frames == 0 || checkpointed != frames {
		t.Fatalf("checkpoint state = busy %d frames %d checkpointed %d", busy, frames, checkpointed)
	}
	pageSize, pageCount, freePages := readTestPageState(t, database)
	compactedBytes := (pageCount - freePages) * pageSize
	walInfo, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("stat retained WAL: %v", err)
	}
	if walInfo.Size() == 0 {
		t.Fatal("retained WAL allocation = 0, want checkpointed allocation")
	}
	policy := balancedMaintenancePolicy()
	policy.MaxSizeBytes = compactedBytes + 1

	status, err := auditmaintenance.ReadStatus(t.Context(), path, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.SizeState != auditmaintenance.SizeStateReclaimPending {
		t.Fatalf("size state = %q, want reclaim_pending", status.SizeState)
	}
}

func TestReadStatusReportsProtectedOnlyConstraint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "protected-only", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("Append protected graph: %v", err)
	}
	pageSize, pageCount, freePages := readTestPageState(t, store.Handle())
	compactedBytes := (pageCount - freePages) * pageSize
	if err := store.Close(); err != nil {
		t.Fatalf("Close protected store: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.MaxSizeBytes = compactedBytes - pageSize

	status, err := auditmaintenance.ReadStatus(t.Context(), path, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.SizeState != auditmaintenance.SizeStateConstrained {
		t.Fatalf("size state = %q, want constrained", status.SizeState)
	}
}

func TestReadStatusReportsFreeAllocationAsReclaimPending(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(t.Context(), `
		create table free_padding (value blob not null);
		insert into free_padding values (zeroblob(2097152));
		drop table free_padding;
	`); err != nil {
		t.Fatalf("create free allocation: %v", err)
	}
	pageSize, pageCount, freePages := readTestPageState(t, database)
	if freePages == 0 {
		t.Fatal("free pages = 0, want retained free allocation")
	}
	compactedBytes := (pageCount - freePages) * pageSize
	physicalBytes := pageCount * pageSize
	if err := database.Close(); err != nil {
		t.Fatalf("close free allocation database: %v", err)
	}
	policy := fixture.policy
	policy.MaxSizeBytes = compactedBytes + (physicalBytes-compactedBytes)/2

	status, err := auditmaintenance.ReadStatus(
		t.Context(), fixture.path, policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.SizeState != auditmaintenance.SizeStateReclaimPending {
		t.Fatalf("size state = %q, want reclaim_pending", status.SizeState)
	}
}

func TestReadStatusReportsConstraintWhenEligibleGraphIsTooSmall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	protected, err := store.Append(t.Context(), intake.Record{
		EventID: "large-protected", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     bytes.Repeat([]byte("p"), 950_000),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append protected graph: %v", err)
	}
	if protected.EventID == "" {
		t.Fatal("protected event id is empty")
	}
	eligible, err := store.Append(t.Context(), intake.Record{
		EventID: "tiny-eligible", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append eligible graph: %v", err)
	}
	insertCompletedEvaluation(
		t, store.Handle(), eligible.ReceiptID, eligible.EventID, "hot",
	)
	if err := store.Close(); err != nil {
		t.Fatalf("Close mixed store: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.MaxSizeBytes = 1_000_000
	plan, err := auditmaintenance.Preview(t.Context(), path, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.ProtectedBytes >= policy.MaxSizeBytes {
		t.Fatalf(
			"protected estimate = %d, want below %d",
			plan.ProtectedBytes,
			policy.MaxSizeBytes,
		)
	}

	status, err := auditmaintenance.ReadStatus(t.Context(), path, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.SizeState != auditmaintenance.SizeStateConstrained {
		t.Fatalf("size state = %q, want constrained", status.SizeState)
	}
}

func TestReadStatusKeepsSharedAuditEventInProtectedFootprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	protected := insertFixtureGraph(
		t, store, store.Handle(), "shared-protected", time.Now().UTC(), true,
	)
	eligible := insertFixtureGraph(
		t, store, store.Handle(), "shared-eligible", time.Now().UTC(), true,
	)
	if _, err := store.Handle().ExecContext(t.Context(), `
		insert into deferred_audit_outbox (
			receipt_id, event_id, evaluation_id, state, created_at,
			claim_owner, claim_expires_at, claim_attempt
		) values
			(?, ?, ?, 'pending', '2030-08-01T00:00:00Z', '', null, 0),
			(?, ?, ?, 'complete', '2030-08-01T00:00:00Z', '', null, 0);
		insert into deferred_audit_outbox_entries (
			receipt_id, entry_index, audit_event_id, delivered_at,
			payload_recorded, payload_available, payload_state_changed_at
		) values
			(?, 0, 'shared-audit-event', null, 1, 0, '2030-08-01T00:00:00Z'),
			(?, 0, 'shared-audit-event', '2030-08-01T00:00:01Z',
				1, 0, '2030-08-01T00:00:01Z');
		insert into events (event_id, message)
		values ('shared-audit-event', ?)
	`,
		protected.ReceiptID, protected.EventID, "evaluation-"+protected.EventID,
		eligible.ReceiptID, eligible.EventID, "evaluation-"+eligible.EventID,
		protected.ReceiptID, eligible.ReceiptID, bytes.Repeat([]byte("a"), 2<<20),
	); err != nil {
		_ = store.Close()
		t.Fatalf("insert shared audit event graph: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close shared audit event store: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.MaxSizeBytes = 1_000_000

	status, err := auditmaintenance.ReadStatus(t.Context(), path, policy, time.Now().UTC())
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	if status.SizeState != auditmaintenance.SizeStateConstrained {
		t.Fatalf("size state = %q, want constrained", status.SizeState)
	}
}

func readTestPageState(t *testing.T, database *sql.DB) (int64, int64, int64) {
	t.Helper()
	var pageSize int64
	var pageCount int64
	var freePages int64
	if err := database.QueryRowContext(t.Context(), `pragma page_size`).Scan(&pageSize); err != nil {
		t.Fatalf("page size: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `pragma page_count`).Scan(&pageCount); err != nil {
		t.Fatalf("page count: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `pragma freelist_count`).Scan(&freePages); err != nil {
		t.Fatalf("free pages: %v", err)
	}
	return pageSize, pageCount, freePages
}
