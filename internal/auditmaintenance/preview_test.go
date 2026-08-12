package auditmaintenance_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

type maintenanceFixture struct {
	path   string
	policy config.AuditStoragePolicy
	now    time.Time
}

type fileSnapshot struct {
	exists bool
	bytes  []byte
}

func TestPreviewSelectsOnlyEligibleGraphs(t *testing.T) {
	fixture := createMaintenanceFixture(t)

	plan, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.DetailCandidateGraphs != 2 {
		t.Fatalf("detail candidates = %d, want 2", plan.DetailCandidateGraphs)
	}
	if plan.SummaryCandidateGraphs != 1 {
		t.Fatalf("summary candidates = %d, want 1", plan.SummaryCandidateGraphs)
	}
	if plan.ProtectedGraphs != 6 {
		t.Fatalf("protected graphs = %d, want 6", plan.ProtectedGraphs)
	}
	if plan.EstimatedDeleteBytes <= 0 {
		t.Fatalf("estimated delete bytes = %d, want positive", plan.EstimatedDeleteBytes)
	}
}

func TestPreviewSkipsDetailAlreadyExpired(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(t.Context(), `
		delete from intake_event_details where event_id = 'old-detail';
		delete from gate_evaluation_details
		where evaluation_id in (
			select evaluation_id from gate_evaluations where event_id = 'old-detail'
		);
		update intake_event_detail_manifest
		set available_classes_json = '[]', state = 'expired'
		where event_id = 'old-detail';
		update gate_evaluations set detail_state = 'expired'
		where event_id = 'old-detail';
	`); err != nil {
		t.Fatalf("expire old detail: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close maintenance database: %v", err)
	}

	plan, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.DetailCandidateGraphs != 1 {
		t.Fatalf("detail candidates = %d, want only old summary graph", plan.DetailCandidateGraphs)
	}
	if plan.SummaryCandidateGraphs != 1 {
		t.Fatalf("summary candidates = %d, want old summary graph", plan.SummaryCandidateGraphs)
	}
}

func TestPreviewProtectsReceiptUntilHotEvaluationCompletes(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)

	before, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview before hot evaluation: %v", err)
	}
	insertCompletedEvaluation(t, database, receiptID(t, database, "awaiting-hot"), "awaiting-hot", "hot")
	after, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview after hot evaluation: %v", err)
	}
	if before.ProtectedGraphs-after.ProtectedGraphs != 1 {
		t.Fatalf(
			"protected graphs changed from %d to %d, want one graph released",
			before.ProtectedGraphs,
			after.ProtectedGraphs,
		)
	}
	if after.SummaryCandidateGraphs-before.SummaryCandidateGraphs != 1 {
		t.Fatalf(
			"summary candidates changed from %d to %d, want one newly eligible graph",
			before.SummaryCandidateGraphs,
			after.SummaryCandidateGraphs,
		)
	}
}

func TestPreviewProtectsRetryableWorkRegardlessOfAge(t *testing.T) {
	fixture := createMaintenanceFixture(t)

	plan, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.ProtectedGraphs != 6 {
		t.Fatalf("protected graphs = %d, want six pending or retryable graphs", plan.ProtectedGraphs)
	}
	if plan.SummaryCandidateGraphs != 1 {
		t.Fatalf("summary candidates = %d, want retryable graphs excluded", plan.SummaryCandidateGraphs)
	}
}

func TestPreviewUsesOneClockAndPolicySnapshot(t *testing.T) {
	fixture := createMaintenanceFixture(t)

	plan, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if !plan.PlannedAt.Equal(fixture.now) {
		t.Fatalf("planned at = %s, want %s", plan.PlannedAt, fixture.now)
	}
	wantDetailCutoff := fixture.now.Add(-7 * 24 * time.Hour)
	if plan.DetailCutoff == nil || !plan.DetailCutoff.Equal(wantDetailCutoff) {
		t.Fatalf("detail cutoff = %v, want %s", plan.DetailCutoff, wantDetailCutoff)
	}
	wantSummaryCutoff := fixture.now.Add(-30 * 24 * time.Hour)
	if !plan.SummaryCutoff.Equal(wantSummaryCutoff) {
		t.Fatalf("summary cutoff = %s, want %s", plan.SummaryCutoff, wantSummaryCutoff)
	}

	changedPolicy := fixture.policy
	changedPolicy.SummaryRetention = 31 * 24 * time.Hour
	changed, err := auditmaintenance.Preview(
		t.Context(), fixture.path, changedPolicy, fixture.now.Add(time.Hour),
	)
	if err != nil {
		t.Fatalf("Preview changed snapshot: %v", err)
	}
	if changed.PolicyHash == plan.PolicyHash {
		t.Fatal("policy hash did not change with the policy snapshot")
	}
	if !changed.PlannedAt.Equal(fixture.now.Add(time.Hour)) {
		t.Fatalf("changed planned at = %s, want supplied clock", changed.PlannedAt)
	}
}

func TestPreviewComparesReceiptCutoffsByInstant(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	database := store.Handle()
	cutoff := time.Date(2030, 8, 25, 12, 0, 0, 0, time.UTC)
	negativeOffset := insertFixtureGraph(t, store, database, "negative-offset", cutoff, true)
	positiveOffset := insertFixtureGraph(t, store, database, "positive-offset", cutoff, true)
	if _, err := database.ExecContext(t.Context(), `
		update intake_receipts set received_at = ? where receipt_id = ?;
		update intake_receipts set received_at = ? where receipt_id = ?;
	`,
		"2030-08-25T07:00:00.000-05:00", negativeOffset.ReceiptID,
		"2030-08-25T13:00:00+01:00", positiveOffset.ReceiptID,
	); err != nil {
		_ = store.Close()
		t.Fatalf("set equivalent receipt instants: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close offset fixture: %v", err)
	}
	policy := balancedMaintenancePolicy()
	policy.FullDetailRetention = 7 * 24 * time.Hour
	policy.SummaryRetention = 7 * 24 * time.Hour

	plan, err := auditmaintenance.Preview(
		t.Context(), path, policy, cutoff.Add(7*24*time.Hour),
	)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if plan.DetailCandidateGraphs != 0 || plan.SummaryCandidateGraphs != 0 {
		t.Fatalf(
			"detail/summary candidates = %d/%d, want equivalent cutoff instants retained",
			plan.DetailCandidateGraphs,
			plan.SummaryCandidateGraphs,
		)
	}
}

func TestPreviewRejectsInvalidReceiptTimestamp(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database := openReadWriteDatabase(t, fixture.path)
	if _, err := database.ExecContext(
		t.Context(),
		`update intake_receipts set received_at = 'invalid' where receipt_id = (
			select min(receipt_id) from intake_receipts
		)`,
	); err != nil {
		t.Fatalf("set invalid receipt timestamp: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close invalid timestamp fixture: %v", err)
	}

	_, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid receipt timestamp") {
		t.Fatalf("Preview error = %v, want invalid receipt timestamp", err)
	}
}

func TestPreviewDoesNotWriteDatabase(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	before := snapshotSQLiteFiles(t, fixture.path)

	if _, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	); err != nil {
		t.Fatalf("Preview: %v", err)
	}

	after := snapshotSQLiteFiles(t, fixture.path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite files changed during preview\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestImmutableReadOnlyIgnoresCommittedActiveWAL(t *testing.T) {
	path, store := createActiveWALFixture(t)
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close active WAL store: %v", err)
		}
	}()
	uri := url.URL{Scheme: "file", Path: path}
	values := url.Values{}
	values.Set("mode", "ro")
	values.Set("immutable", "1")
	uri.RawQuery = values.Encode()
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		t.Fatalf("open immutable SQLite: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from intake_events where event_id = 'wal-only'`,
	).Scan(&count); err != nil {
		t.Fatalf("query immutable SQLite: %v", err)
	}
	if count != 0 {
		t.Fatalf("immutable active WAL count = %d, want committed WAL ignored", count)
	}
}

func TestPreviewSeesCommittedActiveWALWithoutWritingSource(t *testing.T) {
	path, store := createActiveWALFixture(t)
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close active WAL store: %v", err)
		}
	}()
	before := snapshotSQLiteFiles(t, path)
	policy := balancedMaintenancePolicy()

	plan, err := auditmaintenance.Preview(
		t.Context(), path, policy, time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("Preview active WAL: %v", err)
	}
	if plan.ProtectedGraphs != 1 {
		t.Fatalf("protected graphs = %d, want committed WAL graph", plan.ProtectedGraphs)
	}
	after := snapshotSQLiteFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("active SQLite files changed during preview\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestPreviewStreamsLargeDatabaseDuringContinuousWALWrites(t *testing.T) {
	fixture := createMaintenanceFixture(t)
	database, err := sql.Open("sqlite3", fixture.path)
	if err != nil {
		t.Fatalf("open large fixture: %v", err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if _, err := database.ExecContext(t.Context(), `
		create table snapshot_padding (value blob not null);
		insert into snapshot_padding values (zeroblob(67108864));
		create table snapshot_heartbeat (sequence integer primary key);
	`); err != nil {
		t.Fatalf("create 64 MiB fixture: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatalf("checkpoint large fixture: %v", err)
	}
	stopWrites := make(chan struct{})
	writeResult := make(chan error, 1)
	firstWrite := make(chan struct{})
	go writeSnapshotHeartbeats(database, stopWrites, firstWrite, writeResult)
	<-firstWrite
	writerRunning := true
	defer func() {
		if writerRunning {
			close(stopWrites)
			if err := <-writeResult; err != nil {
				t.Errorf("continuous WAL writer: %v", err)
			}
		}
	}()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	plan, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	)
	close(stopWrites)
	writerRunning = false
	if writeErr := <-writeResult; writeErr != nil {
		t.Fatalf("continuous WAL writer: %v", writeErr)
	}
	if err != nil {
		t.Fatalf("Preview 64 MiB active WAL database: %v", err)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	if plan.ProtectedGraphs < 6 {
		t.Fatalf("protected graphs = %d, want committed graph state", plan.ProtectedGraphs)
	}
	const allocationLimit = 32 << 20
	allocated := after.TotalAlloc - before.TotalAlloc
	if allocated > allocationLimit {
		t.Fatalf("preview allocated %d bytes, want at most %d", allocated, allocationLimit)
	}
	beforeFiles := snapshotSQLiteFiles(t, fixture.path)
	if _, err := auditmaintenance.Preview(
		t.Context(), fixture.path, fixture.policy, fixture.now,
	); err != nil {
		t.Fatalf("Preview stable large database: %v", err)
	}
	afterFiles := snapshotSQLiteFiles(t, fixture.path)
	if !reflect.DeepEqual(afterFiles, beforeFiles) {
		t.Fatalf("SQLite files changed during stable preview\nbefore: %#v\nafter:  %#v", beforeFiles, afterFiles)
	}
}

func writeSnapshotHeartbeats(
	database *sql.DB,
	stop <-chan struct{},
	firstWrite chan<- struct{},
	result chan<- error,
) {
	sequence := 1
	first := true
	for {
		select {
		case <-stop:
			result <- nil
			return
		default:
		}
		if _, err := database.Exec(
			`insert into snapshot_heartbeat (sequence) values (?)`, sequence,
		); err != nil {
			result <- err
			return
		}
		if first {
			close(firstWrite)
			first = false
		}
		sequence++
	}
}

func createMaintenanceFixture(t *testing.T) maintenanceFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	database := store.Handle()
	now := time.Date(2030, 9, 1, 12, 0, 0, 0, time.UTC)

	insertFixtureGraph(t, store, database, "old-detail", now.Add(-10*24*time.Hour), true)
	insertFixtureGraph(t, store, database, "recent-detail", now.Add(-24*time.Hour), true)
	insertFixtureGraph(t, store, database, "old-summary", now.Add(-40*24*time.Hour), true)
	insertFixtureGraph(t, store, database, "awaiting-hot", now.Add(-50*24*time.Hour), false)
	insertPendingDeferredGraph(t, store, database, "pending-deferred", now, false)
	insertPendingDeferredGraph(t, store, database, "expired-evaluation-claim", now, true)
	insertPendingOutboxGraph(t, store, database, "pending-audit", now, false, false)
	insertPendingOutboxGraph(t, store, database, "expired-audit-claim", now, true, false)
	insertPendingOutboxGraph(t, store, database, "partly-delivered", now, false, true)

	if _, err := database.ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		t.Fatalf("checkpoint fixture: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close fixture: %v", err)
	}

	return maintenanceFixture{
		path:   path,
		now:    now,
		policy: balancedMaintenancePolicy(),
	}
}

func createActiveWALFixture(t *testing.T) (string, *intake.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_autocheckpoint = 0`); err != nil {
		_ = store.Close()
		t.Fatalf("disable WAL auto-checkpoint: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		_ = store.Close()
		t.Fatalf("checkpoint active fixture: %v", err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "wal-only", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("Append WAL-only graph: %v", err)
	}
	return path, store
}

func balancedMaintenancePolicy() config.AuditStoragePolicy {
	return config.AuditStoragePolicy{
		Profile:                 config.AuditStorageProfileBalanced,
		MaintenanceInterval:     24 * time.Hour,
		MaintenanceBatchRows:    1000,
		CompactAfterMaintenance: true,
		FullDetailRetention:     7 * 24 * time.Hour,
		SummaryRetention:        30 * 24 * time.Hour,
		Detail: config.AuditStorageDetailPolicy{
			WireInput: true, NormalizedInput: true, ProviderEvidence: true,
			EnvironmentEvidence: true, EvaluationContent: true,
		},
	}
}

func insertFixtureGraph(
	t *testing.T,
	store *intake.Store,
	database *sql.DB,
	eventID string,
	receivedAt time.Time,
	completedHot bool,
) intake.AppendResult {
	t.Helper()
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: eventID, RecordedAt: receivedAt, System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		t.Fatalf("Append %s: %v", eventID, err)
	}
	if _, err := database.ExecContext(
		t.Context(),
		`update intake_receipts set received_at = ? where receipt_id = ?`,
		receivedAt.Format(time.RFC3339Nano),
		receipt.ReceiptID,
	); err != nil {
		t.Fatalf("set receipt time %s: %v", eventID, err)
	}
	if completedHot {
		insertCompletedEvaluation(t, database, receipt.ReceiptID, eventID, "hot")
	}
	return receipt
}

func insertPendingDeferredGraph(
	t *testing.T,
	store *intake.Store,
	database *sql.DB,
	eventID string,
	now time.Time,
	expiredClaim bool,
) {
	t.Helper()
	receipt := insertFixtureGraph(t, store, database, eventID, now.Add(-50*24*time.Hour), true)
	claimExpiry := now.Add(time.Hour)
	if expiredClaim {
		claimExpiry = now.Add(-time.Hour)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into intake_deferred (
			receipt_id, event_id, state, pending_at, claim_owner,
			claim_expires_at, claim_attempt
		) values (?, ?, 'pending', ?, 'worker', ?, 1)
	`, receipt.ReceiptID, eventID, now.Add(-48*time.Hour).Format(time.RFC3339Nano),
		claimExpiry.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert deferred %s: %v", eventID, err)
	}
}

func insertPendingOutboxGraph(
	t *testing.T,
	store *intake.Store,
	database *sql.DB,
	eventID string,
	now time.Time,
	expiredClaim bool,
	partlyDelivered bool,
) {
	t.Helper()
	receipt := insertFixtureGraph(t, store, database, eventID, now.Add(-50*24*time.Hour), true)
	evaluationID := "evaluation-" + eventID
	claimExpiry := now.Add(time.Hour)
	if expiredClaim {
		claimExpiry = now.Add(-time.Hour)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into deferred_audit_outbox (
			receipt_id, event_id, evaluation_id, state, created_at,
			claim_owner, claim_expires_at, claim_attempt
		) values (?, ?, ?, 'pending', ?, 'audit-worker', ?, 1)
	`, receipt.ReceiptID, eventID, evaluationID,
		now.Add(-48*time.Hour).Format(time.RFC3339Nano), claimExpiry.Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert outbox %s: %v", eventID, err)
	}
	insertOutboxEntry(t, database, receipt.ReceiptID, 0, eventID+"-audit-0", true)
	if partlyDelivered {
		insertOutboxEntry(t, database, receipt.ReceiptID, 1, eventID+"-audit-1", false)
	}
}

func insertOutboxEntry(
	t *testing.T,
	database *sql.DB,
	receiptID int64,
	entryIndex int,
	auditEventID string,
	delivered bool,
) {
	t.Helper()
	var deliveredAt *string
	if delivered {
		value := "2030-08-01T00:00:00Z"
		deliveredAt = &value
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into deferred_audit_outbox_entries (
			receipt_id, entry_index, audit_event_id, delivered_at,
			payload_recorded, payload_available, payload_state_changed_at
		) values (?, ?, ?, ?, 1, 1, '2030-08-01T00:00:00Z')
	`, receiptID, entryIndex, auditEventID, deliveredAt); err != nil {
		t.Fatalf("insert outbox entry %s: %v", auditEventID, err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into deferred_audit_outbox_entry_details (
			receipt_id, entry_index, payload_json
		) values (?, ?, ?)
	`, receiptID, entryIndex, []byte(`{"message":"audit"}`)); err != nil {
		t.Fatalf("insert outbox detail %s: %v", auditEventID, err)
	}
}

func insertCompletedEvaluation(
	t *testing.T,
	database *sql.DB,
	receiptID int64,
	eventID string,
	mode string,
) {
	t.Helper()
	evaluationID := "evaluation-" + eventID
	completedAt := "2030-07-01T00:00:00Z"
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		) values (?, ?, ?, 1, ?, 'config', 'version', 'commit', 'build',
			'input', ?, ?, 'allow', 'deterministic', 'allow', 0, 1, 0, 0, 'available')
	`, evaluationID, receiptID, eventID, mode, completedAt, completedAt); err != nil {
		t.Fatalf("insert evaluation %s: %v", eventID, err)
	}
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluation_details (evaluation_id, error_json)
		values (?, ?)
	`, evaluationID, []byte(`{"error":null}`)); err != nil {
		t.Fatalf("insert evaluation detail %s: %v", eventID, err)
	}
}

func receiptID(t *testing.T, database *sql.DB, eventID string) int64 {
	t.Helper()
	var receiptID int64
	if err := database.QueryRowContext(
		t.Context(), `select receipt_id from intake_receipts where event_id = ?`, eventID,
	).Scan(&receiptID); err != nil {
		t.Fatalf("read receipt %s: %v", eventID, err)
	}
	return receiptID
}

func openReadWriteDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close SQLite: %v", err)
		}
	})
	return database
}

func snapshotSQLiteFiles(t *testing.T, path string) map[string]fileSnapshot {
	t.Helper()
	snapshots := make(map[string]fileSnapshot)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				snapshots[candidate] = fileSnapshot{exists: false, bytes: nil}
				continue
			}
			t.Fatalf("ReadFile %s: %v", candidate, err)
		}
		digest := sha256.Sum256(body)
		snapshots[candidate] = fileSnapshot{
			exists: true,
			bytes:  []byte(hex.EncodeToString(digest[:])),
		}
	}
	return snapshots
}
