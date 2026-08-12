package auditmaintenance_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"sync"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/intake"
)

const sizedGraphBytes = 600_000

const highCardinalityGraphCount = 2000

func TestSizeRetentionDeletesOldestEligibleGraphFirst(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "oldest", age: 3 * time.Hour, protected: false},
		{eventID: "middle", age: 2 * time.Hour, protected: false},
		{eventID: "youngest", age: time.Hour, protected: false},
	})
	initial := measureSize(t, fixture.path)
	fixture.policy.MaxSizeBytes = initial.CompactedUsageBytes - sizedGraphBytes/2

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != 1 {
		t.Fatalf("summary graphs = %d, want 1", result.SummaryGraphs)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "oldest", 0)
	assertGraphCount(t, database, "middle", 1)
	assertGraphCount(t, database, "youngest", 1)
}

func TestSizeRetentionHighCardinalityCompletesWithinBound(t *testing.T) {
	fixture := createHighCardinalitySizeFixture(t, highCardinalityGraphCount)
	fixture.policy.MaxSizeBytes = 1
	ctx, cancel := context.WithTimeout(t.Context(), sizePerformanceTimeout())
	defer cancel()

	result, err := auditmaintenance.Apply(ctx, applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != highCardinalityGraphCount {
		t.Fatalf(
			"summary graphs = %d, want %d",
			result.SummaryGraphs,
			highCardinalityGraphCount,
		)
	}
}

func sizePerformanceTimeout() time.Duration {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				return 30 * time.Second
			}
		}
	}
	return 20 * time.Second
}

func TestSizePreviewStopsCandidateCountAtMeasuredTarget(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "oldest", age: 3 * time.Hour, protected: false},
		{eventID: "middle", age: 2 * time.Hour, protected: false},
		{eventID: "youngest", age: time.Hour, protected: false},
	})
	initial := measureSize(t, fixture.path)
	fixture.policy.MaxSizeBytes = initial.CompactedUsageBytes - sizedGraphBytes/2
	before := snapshotSQLiteFiles(t, fixture.path)

	plan, err := auditmaintenance.Preview(
		t.Context(),
		fixture.path,
		fixture.policy,
		fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.SummaryCandidateGraphs != 1 {
		t.Fatalf("summary candidates = %d, want 1", plan.SummaryCandidateGraphs)
	}
	after := snapshotSQLiteFiles(t, fixture.path)
	if !mapsEqual(after, before) {
		t.Fatalf("preview changed source SQLite files: before %#v after %#v", before, after)
	}
	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != plan.SummaryCandidateGraphs ||
		result.Plan.SummaryCandidateGraphs != plan.SummaryCandidateGraphs {
		t.Fatalf(
			"apply count/plan = %d/%d, want preview %d",
			result.SummaryGraphs,
			result.Plan.SummaryCandidateGraphs,
			plan.SummaryCandidateGraphs,
		)
	}
}

func TestSizeRetentionOrdersByLatestReceipt(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "old-first-new-latest", age: 3 * time.Hour, protected: false},
		{eventID: "older-latest", age: 2 * time.Hour, protected: false},
	})
	store, err := intake.OpenSQLite(t.Context(), fixture.path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "old-first-new-latest", RecordedAt: fixture.now.Add(-30 * time.Minute),
		System: "codex", SessionID: "session", EventName: "PreToolUse",
		ToolName: "exec_command", RawPayload: bytes.Repeat([]byte("y"), sizedGraphBytes),
		NormalizedJSON: json.RawMessage(`{"command":"make test"}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append latest receipt: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(),
		`update intake_receipts set received_at = ? where receipt_id = ?`,
		fixture.now.Add(-30*time.Minute).Format(time.RFC3339Nano), receipt.ReceiptID,
	); err != nil {
		_ = store.Close()
		t.Fatalf("set latest receipt time: %v", err)
	}
	insertSizedEvaluation(t, store.Handle(), receipt.ReceiptID, receipt.EventID, "latest")
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	initial := measureSize(t, fixture.path)
	fixture.policy.MaxSizeBytes = initial.CompactedUsageBytes - sizedGraphBytes/2

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != 1 {
		t.Fatalf("summary graphs = %d, want 1", result.SummaryGraphs)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "older-latest", 0)
	assertGraphCount(t, database, "old-first-new-latest", 1)
}

func TestSizeRetentionRunsAfterAgeRetention(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "age-expired", age: 40 * 24 * time.Hour, protected: false},
		{eventID: "recent", age: time.Hour, protected: false},
	})
	initial := measureSize(t, fixture.path)
	fixture.policy.MaxSizeBytes = initial.CompactedUsageBytes - sizedGraphBytes/2

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != 1 {
		t.Fatalf("summary graphs = %d, want age phase to meet target with one graph", result.SummaryGraphs)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "age-expired", 0)
	assertGraphCount(t, database, "recent", 1)
}

func TestSizeRetentionStopsAtProtectedData(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "eligible", age: 2 * time.Hour, protected: false},
		{eventID: "protected", age: time.Hour, protected: true},
	})
	fixture.policy.MaxSizeBytes = 1

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SizeState != auditmaintenance.SizeStateConstrained {
		t.Fatalf("size state = %q, want constrained", result.SizeState)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "eligible", 0)
	assertGraphCount(t, database, "protected", 1)
}

func TestSizeRetentionCountsGraphProtectedAfterPlanning(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "oldest", age: 2 * time.Hour, protected: false},
		{eventID: "newly-protected", age: time.Hour, protected: false},
	})
	fixture.policy.MaxSizeBytes = 1
	database := openReadWriteDatabase(t, fixture.path)
	var protectErr error
	var protectOnce sync.Once
	log := slog.New(checkpointStartHandler{
		start: func() {
			protectOnce.Do(func() {
				receipt := receiptID(t, database, "newly-protected")
				_, protectErr = database.ExecContext(t.Context(), `
					insert into intake_deferred (
						receipt_id, event_id, state, pending_at, claim_attempt
					) values (?, 'newly-protected', 'pending', ?, 0)
				`, receipt, fixture.now.Format(time.RFC3339Nano))
			})
		},
		release: func() {},
	})
	options := applyOptions(fixture)
	options.Log = log

	result, err := auditmaintenance.Apply(t.Context(), options)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if protectErr != nil {
		t.Fatalf("protect graph during size retention: %v", protectErr)
	}
	if result.SizeState != auditmaintenance.SizeStateConstrained {
		t.Fatalf("size state = %q, want constrained", result.SizeState)
	}
	assertGraphCount(t, database, "oldest", 0)
	assertGraphCount(t, database, "newly-protected", 1)
}

func TestSizeRetentionStopsWhenCompactedUsageMeetsTarget(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "oldest", age: 2 * time.Hour, protected: false},
		{eventID: "youngest", age: time.Hour, protected: false},
	})
	initial := measureSize(t, fixture.path)
	fixture.policy.MaxSizeBytes = initial.CompactedUsageBytes - sizedGraphBytes/2

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	measured := measureSize(t, fixture.path)
	if measured.CompactedUsageBytes > fixture.policy.MaxSizeBytes {
		t.Fatalf("compacted usage = %d, want at most %d", measured.CompactedUsageBytes, fixture.policy.MaxSizeBytes)
	}
	if result.SummaryGraphs != 1 {
		t.Fatalf("summary graphs = %d, want 1", result.SummaryGraphs)
	}
}

func TestSizeRetentionDoesNotDeleteForUnreclaimedFreePages(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "eligible", age: time.Hour, protected: false},
	})
	database := openReadWriteDatabase(t, fixture.path)
	createFreeAllocation(t, database)
	if err := database.Close(); err != nil {
		t.Fatalf("close free allocation database: %v", err)
	}
	size := measureSize(t, fixture.path)
	physicalBytes := size.DatabaseBytes + size.WALBytes
	fixture.policy.MaxSizeBytes = size.CompactedUsageBytes +
		(physicalBytes-size.CompactedUsageBytes)/2

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SizeState != auditmaintenance.SizeStateReclaimPending {
		t.Fatalf("size state = %q, want reclaim_pending", result.SizeState)
	}
	database = openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "eligible", 1)
}

func TestSizeRetentionIgnoresCheckpointedWALAllocation(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "eligible", age: time.Hour, protected: false},
	})
	store, err := intake.OpenSQLite(t.Context(), fixture.path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_autocheckpoint = 0`); err != nil {
		_ = store.Close()
		t.Fatalf("disable auto-checkpoint: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `
		create table wal_padding (value blob not null);
		insert into wal_padding values (zeroblob(2097152));
		drop table wal_padding;
		pragma wal_checkpoint(passive);
	`); err != nil {
		_ = store.Close()
		t.Fatalf("create checkpointed WAL allocation: %v", err)
	}
	size := measureSize(t, fixture.path)
	if size.WALBytes == 0 {
		_ = store.Close()
		t.Fatal("WAL bytes = 0, want retained checkpointed allocation")
	}
	fixture.policy.MaxSizeBytes = size.CompactedUsageBytes

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		_ = store.Close()
		t.Fatalf("Apply: %v", err)
	}
	if result.SummaryGraphs != 0 {
		_ = store.Close()
		t.Fatalf("summary graphs = %d, want 0", result.SummaryGraphs)
	}
	if result.SizeState != auditmaintenance.SizeStateReclaimPending {
		_ = store.Close()
		t.Fatalf("size state = %q, want reclaim_pending", result.SizeState)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "eligible", 1)
}

func TestMeasureDatabaseSizeCountsReaderPinnedLiveWALFrames(t *testing.T) {
	fixture := createSizeFixture(t, nil)
	store, err := intake.OpenSQLite(t.Context(), fixture.path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() { _ = store.Close() }()
	if _, err := store.Handle().ExecContext(t.Context(), `
		pragma wal_autocheckpoint = 0;
		pragma wal_checkpoint(truncate);
	`); err != nil {
		t.Fatalf("prepare WAL: %v", err)
	}
	appendSizedGraph(t, store, "reader-end-mark", fixture.now.Add(-2*time.Hour))
	reader := openReadWriteDatabase(t, fixture.path)
	connection, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve reader: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(t.Context(), `begin`); err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	defer func() { _, _ = connection.ExecContext(t.Context(), `rollback`) }()
	var count int
	if err := connection.QueryRowContext(t.Context(), `select count(*) from intake_events`).Scan(&count); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}
	appendSizedGraph(t, store, "live-wal", fixture.now.Add(-time.Hour))

	size := measureSize(t, fixture.path)
	allocatedUsage := (size.PageCount - size.FreePages) * size.PageSizeBytes
	if size.CompactedUsageBytes <= allocatedUsage {
		t.Fatalf(
			"compacted usage = %d, want more than allocated pages %d for live WAL frames",
			size.CompactedUsageBytes,
			allocatedUsage,
		)
	}
}

func TestSizeRetentionDefersForCompetingCheckpoint(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "eligible", age: time.Hour, protected: false},
	})
	fixture.policy.MaxSizeBytes = 1
	writer := openReadWriteDatabase(t, fixture.path)
	if _, err := writer.ExecContext(t.Context(), `
		pragma wal_autocheckpoint = 0;
		pragma wal_checkpoint(truncate);
	`); err != nil {
		t.Fatalf("prepare WAL: %v", err)
	}
	reader := openReadWriteDatabase(t, fixture.path)
	readerConnection, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve reader: %v", err)
	}
	defer func() { _ = readerConnection.Close() }()
	if _, err := readerConnection.ExecContext(t.Context(), `begin`); err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	readerActive := true
	defer func() {
		if readerActive {
			_, _ = readerConnection.ExecContext(t.Context(), `rollback`)
		}
	}()
	var count int
	if err := readerConnection.QueryRowContext(
		t.Context(),
		`select count(*) from intake_events`,
	).Scan(&count); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}
	if _, err := writer.ExecContext(t.Context(), `
		update intake_event_details set content = content || zeroblob(4096)
		where event_id = 'eligible'
	`); err != nil {
		t.Fatalf("write live WAL frame: %v", err)
	}
	checkpoint := openReadWriteDatabase(t, fixture.path)
	if _, err := checkpoint.ExecContext(t.Context(), `pragma busy_timeout = 5000`); err != nil {
		t.Fatalf("set checkpoint timeout: %v", err)
	}
	checkpointResult := make(chan error, 1)
	var startCheckpoint sync.Once
	var releaseErr error
	log := slog.New(checkpointStartHandler{
		start: func() {
			startCheckpoint.Do(func() {
				go func() {
					var busy int64
					var frames int64
					var checkpointed int64
					checkpointResult <- checkpoint.QueryRowContext(
						t.Context(),
						`pragma wal_checkpoint(truncate)`,
					).Scan(&busy, &frames, &checkpointed)
				}()
				waitForCheckpointContention(t, fixture.path)
			})
		},
		release: func() {
			releaseErr = func() error {
				_, rollbackErr := readerConnection.ExecContext(t.Context(), `rollback`)
				return rollbackErr
			}()
			readerActive = false
		},
	})
	options := applyOptions(fixture)
	options.Log = log

	result, err := auditmaintenance.Apply(t.Context(), options)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.Result != "deferred" ||
		!errors.Is(result.Err, auditmaintenance.ErrMaintenanceBusy) {
		t.Fatalf("result = %#v, want deferred busy", result)
	}
	if result.SizeState != auditmaintenance.SizeStateUnknown {
		t.Fatalf("size state = %q, want unknown", result.SizeState)
	}
	if releaseErr != nil {
		t.Fatalf("release reader: %v", releaseErr)
	}
	assertGraphCount(t, writer, "eligible", 1)
	assertLastRunResult(t, writer, "deferred", "busy")
	var leases int
	if err := writer.QueryRowContext(
		t.Context(),
		`select count(*) from audit_maintenance_lease`,
	).Scan(&leases); err != nil {
		t.Fatalf("count leases: %v", err)
	}
	if leases != 0 {
		t.Fatalf("leases = %d, want 0", leases)
	}
	if err := <-checkpointResult; err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

func TestZeroSizeTargetDisablesSizePruning(t *testing.T) {
	fixture := createSizeFixture(t, []sizedGraphSpec{
		{eventID: "eligible", age: time.Hour, protected: false},
	})
	fixture.policy.MaxSizeBytes = 0

	result, err := auditmaintenance.Apply(t.Context(), applyOptions(fixture))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.SizeState != auditmaintenance.SizeStateDisabled {
		t.Fatalf("size state = %q, want disabled", result.SizeState)
	}
	database := openReadWriteDatabase(t, fixture.path)
	assertGraphCount(t, database, "eligible", 1)
}

type sizedGraphSpec struct {
	eventID   string
	age       time.Duration
	protected bool
}

func createSizeFixture(t *testing.T, specs []sizedGraphSpec) maintenanceFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	for _, spec := range specs {
		receipt, appendErr := store.Append(t.Context(), intake.Record{
			EventID: spec.eventID, RecordedAt: now.Add(-spec.age), System: "codex",
			SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
			RawPayload:     bytes.Repeat([]byte("x"), sizedGraphBytes),
			NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
		})
		if appendErr != nil {
			_ = store.Close()
			t.Fatalf("Append %s: %v", spec.eventID, appendErr)
		}
		if _, updateErr := store.Handle().ExecContext(t.Context(),
			`update intake_receipts set received_at = ? where receipt_id = ?`,
			now.Add(-spec.age).Format(time.RFC3339Nano), receipt.ReceiptID,
		); updateErr != nil {
			_ = store.Close()
			t.Fatalf("set receipt time %s: %v", spec.eventID, updateErr)
		}
		if !spec.protected {
			insertCompletedEvaluation(t, store.Handle(), receipt.ReceiptID, spec.eventID, "hot")
		}
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		_ = store.Close()
		t.Fatalf("checkpoint size fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.FullDetailRetention = 365 * 24 * time.Hour
	policy.SummaryRetention = 30 * 24 * time.Hour
	return maintenanceFixture{path: path, policy: policy, now: now}
}

func createHighCardinalitySizeFixture(t *testing.T, graphCount int) maintenanceFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	database := store.Handle()
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)
	if _, err := database.ExecContext(t.Context(), `
		with recursive sequence(value) as (
			select 1 union all select value + 1 from sequence where value < ?
		)
		insert into intake_events (
			event_id, schema_version, recorded_at, system, session_id, turn_id,
			event_name, tool_name, tool_use_id, cwd, effective_cwd, command,
			file_path, raw_payload_hash
		)
		select printf('size-%05d', value), 1, ?, 'codex', 'session', '',
			'PreToolUse', 'exec_command', '', '', '', '', '', ''
		from sequence
	`, graphCount, now.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		_ = store.Close()
		t.Fatalf("insert size events: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into intake_receipts(event_id, received_at)
		select event_id, ? from intake_events where event_id like 'size-%'
	`, now.Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		_ = store.Close()
		t.Fatalf("insert size receipts: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		)
		select 'evaluation-' || receipt.event_id, receipt.receipt_id, receipt.event_id,
			1, 'hot', 'config', 'version', 'commit', 'build', 'input', ?, ?,
			'allow', 'deterministic', 'allow', 0, 1, 0, 0, 'available'
		from intake_receipts receipt where receipt.event_id like 'size-%'
	`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		_ = store.Close()
		t.Fatalf("insert size evaluations: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		_ = store.Close()
		t.Fatalf("checkpoint size fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.FullDetailRetention = 365 * 24 * time.Hour
	policy.SummaryRetention = 30 * 24 * time.Hour
	return maintenanceFixture{path: path, policy: policy, now: now}
}

func measureSize(t *testing.T, path string) auditmaintenance.DatabaseSize {
	t.Helper()
	size, err := auditmaintenance.MeasureDatabaseSize(path)
	if err != nil {
		t.Fatalf("MeasureDatabaseSize: %v", err)
	}
	return size
}

func createFreeAllocation(t *testing.T, database *sql.DB) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
		create table free_padding (value blob not null);
		insert into free_padding values (zeroblob(2097152));
		drop table free_padding;
	`); err != nil {
		t.Fatalf("create free allocation: %v", err)
	}
}

func mapsEqual(left map[string]fileSnapshot, right map[string]fileSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		rightValue, ok := right[key]
		if !ok || leftValue.exists != rightValue.exists ||
			!bytes.Equal(leftValue.bytes, rightValue.bytes) {
			return false
		}
	}
	return true
}

func appendSizedGraph(t *testing.T, store *intake.Store, eventID string, receivedAt time.Time) {
	t.Helper()
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: eventID, RecordedAt: receivedAt, System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     bytes.Repeat([]byte("z"), sizedGraphBytes),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
	insertCompletedEvaluation(t, store.Handle(), receipt.ReceiptID, eventID, "hot")
}

func insertSizedEvaluation(
	t *testing.T,
	database *sql.DB,
	receiptID int64,
	eventID string,
	suffix string,
) {
	t.Helper()
	evaluationID := "evaluation-" + eventID + "-" + suffix
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		) values (?, ?, ?, 1, 'hot', 'config', 'version', 'commit', 'build',
			'input', ?, ?, 'allow', 'deterministic', 'allow', 0, 1, 0, 0, 'available')
	`, evaluationID, receiptID, eventID, "2030-08-01T00:00:00Z", "2030-08-01T00:00:00Z"); err != nil {
		t.Fatalf("insert evaluation %s: %v", eventID, err)
	}
}

func waitForCheckpointContention(t *testing.T, path string) {
	t.Helper()
	probe := openReadWriteDatabase(t, path)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var busy int64
		var frames int64
		var checkpointed int64
		err := probe.QueryRowContext(t.Context(), `pragma wal_checkpoint(passive)`).Scan(
			&busy,
			&frames,
			&checkpointed,
		)
		if err == nil && busy != 0 && frames < 0 && checkpointed < 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("checkpoint contention was not observed")
}

type checkpointStartHandler struct {
	start   func()
	release func()
}

func (handler checkpointStartHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler checkpointStartHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == auditmaintenance.TestLogMeasureAuditSize {
		handler.start()
	}
	if record.Message == auditmaintenance.TestLogAuditSizeMeasurementFailed {
		handler.release()
	}
	return nil
}

func (handler checkpointStartHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler checkpointStartHandler) WithGroup(string) slog.Handler {
	return handler
}
