package auditmaintenance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/intake"
)

func TestCompactDryRunReportsFreePagesWithoutWriting(t *testing.T) {
	path := createCompactFixture(t, 2<<20)
	before := snapshotCompactFiles(t, path)

	plan, err := PreviewCompact(t.Context(), path, 11)
	if err != nil {
		t.Fatalf("PreviewCompact: %v", err)
	}
	if plan.AutoVacuumMode != 2 || plan.FreePages <= 11 || plan.PagesToReclaim != 11 {
		t.Fatalf("compact plan = %#v, want incremental mode and 11 bounded pages", plan)
	}
	if plan.FullModeRequired {
		t.Fatal("full mode required for incremental database")
	}
	after := snapshotCompactFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("dry-run changed SQLite files: before %#v after %#v", before, after)
	}
}

func TestCompactApplyCheckpointsAndReclaimsBoundedPages(t *testing.T) {
	path := createCompactFixture(t, 2<<20)
	beforePages, beforeFree := compactPageState(t, path)
	beforeMain := compactMainBytes(t, path)
	options := compactApplyOptions(path, 13)

	result, err := Compact(t.Context(), options)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	afterPages, afterFree := compactPageState(t, path)
	reclaimedPages := beforePages - afterPages
	if reclaimedPages <= 0 || reclaimedPages > 13 {
		t.Fatalf("reclaimed pages = %d, want 1 through 13", reclaimedPages)
	}
	if beforeFree-afterFree != reclaimedPages {
		t.Fatalf("free/page reduction = %d/%d, want equal", beforeFree-afterFree, reclaimedPages)
	}
	if result.ReclaimedBytes <= 0 || result.CompactPlan.PagesToReclaim != 13 {
		t.Fatalf("compact result = %#v, want measured bytes and 13 planned pages", result)
	}
	afterMain := compactMainBytes(t, path)
	if result.ReclaimedBytes != max(beforeMain-afterMain, 0) {
		t.Fatalf(
			"reclaimed bytes = %d, want measured main database reduction %d",
			result.ReclaimedBytes,
			max(beforeMain-afterMain, 0),
		)
	}
}

func TestCompactApplyPreservesIntegrityAndRows(t *testing.T) {
	path := createCompactFixture(t, 1<<20)

	if _, err := Compact(t.Context(), compactApplyOptions(path, 1000)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	database := openCompactDatabase(t, path)
	var rows int
	if err := database.QueryRowContext(
		t.Context(), `select count(*) from intake_events where event_id = 'retained'`,
	).Scan(&rows); err != nil {
		t.Fatalf("count retained rows: %v", err)
	}
	if rows != 1 {
		t.Fatalf("retained rows = %d, want 1", rows)
	}
	assertCompactIntegrity(t, database)
}

func TestCompactReportsFullCompactionNeededForLegacyDatabase(t *testing.T) {
	path := createLegacyCompactFixture(t)
	before := snapshotCompactFiles(t, path)

	plan, err := PreviewCompact(t.Context(), path, 100)
	if err != nil {
		t.Fatalf("PreviewCompact: %v", err)
	}
	if plan.AutoVacuumMode != 0 || !plan.FullModeRequired || plan.PagesToReclaim != 0 {
		t.Fatalf("legacy compact plan = %#v, want full mode required", plan)
	}
	after := snapshotCompactFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("legacy preview changed SQLite files: before %#v after %#v", before, after)
	}
}

func TestMaintenanceCompactsOnlyWhenConfigured(t *testing.T) {
	withoutPath := createCompactMaintenanceFixture(t)
	without := compactApplyOptions(withoutPath, 1000)
	without.Policy.CompactAfterMaintenance = false
	withoutResult, err := Apply(t.Context(), without)
	if err != nil {
		t.Fatalf("Apply without compaction: %v", err)
	}
	_, withoutFree := compactPageState(t, withoutPath)
	if withoutResult.ReclaimedBytes != 0 || withoutFree == 0 {
		t.Fatalf("without compaction result/free = %#v/%d, want zero bytes and free pages", withoutResult, withoutFree)
	}

	withPath := createCompactMaintenanceFixture(t)
	with := compactApplyOptions(withPath, 1000)
	with.Policy.CompactAfterMaintenance = true
	withResult, err := Apply(t.Context(), with)
	if err != nil {
		t.Fatalf("Apply with compaction: %v", err)
	}
	_, withFree := compactPageState(t, withPath)
	if withResult.ReclaimedBytes <= 0 || withFree >= withoutFree {
		t.Fatalf("with compaction result/free = %#v/%d, want reclaimed bytes and fewer free pages", withResult, withFree)
	}
	assertRunReclaimedBytes(t, withPath, withResult.RunID, withResult.ReclaimedBytes)
}

func TestCompactApplyRejectsOverlappingMaintenance(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	database := openCompactDatabase(t, path)
	if _, err := database.ExecContext(t.Context(), `
		insert into audit_maintenance_lease(singleton, owner, run_id, expires_at)
		values (1, 'other', 'other-run', ?)
	`, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("insert competing lease: %v", err)
	}

	result, err := Compact(t.Context(), compactApplyOptions(path, 100))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Result != "deferred" || !errors.Is(result.Err, ErrMaintenanceBusy) {
		t.Fatalf("result = %#v, want deferred busy", result)
	}
}

func TestCompactApplyDefersWhenPassiveCheckpointIsBusy(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	writer := openCompactDatabase(t, path)
	writer.SetMaxOpenConns(1)
	if _, err := writer.ExecContext(t.Context(), `
		pragma wal_autocheckpoint = 0;
		pragma wal_checkpoint(truncate);
	`); err != nil {
		t.Fatalf("prepare WAL: %v", err)
	}
	reader := openCompactDatabase(t, path)
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
	var rows int
	if err := readerConnection.QueryRowContext(
		t.Context(), `select count(*) from intake_events`,
	).Scan(&rows); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}
	if _, err := writer.ExecContext(t.Context(), `
		update intake_events set command = command || 'x' where event_id = 'retained'
	`); err != nil {
		t.Fatalf("write live WAL frame: %v", err)
	}
	checkpoint := openCompactDatabase(t, path)
	if _, err := checkpoint.ExecContext(t.Context(), `pragma busy_timeout = 5000`); err != nil {
		t.Fatalf("set checkpoint timeout: %v", err)
	}
	checkpointResult := make(chan error, 1)
	var startCheckpoint sync.Once
	var releaseErr error
	log := slog.New(compactCheckpointHandler{
		start: func() {
			startCheckpoint.Do(func() {
				go func() {
					var busy int64
					var frames int64
					var checkpointed int64
					checkpointResult <- checkpoint.QueryRowContext(
						t.Context(), `pragma wal_checkpoint(truncate)`,
					).Scan(&busy, &frames, &checkpointed)
				}()
				waitForCompactCheckpointContention(t, path)
			})
		},
		release: func() {
			_, releaseErr = readerConnection.ExecContext(t.Context(), `rollback`)
			readerActive = false
		},
	})
	options := compactApplyOptions(path, 100)
	options.Log = log

	result, err := Compact(t.Context(), options)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if result.Result != "deferred" || !errors.Is(result.Err, ErrMaintenanceBusy) {
		t.Fatalf("result = %#v, want deferred busy", result)
	}
	if releaseErr != nil {
		t.Fatalf("release reader: %v", releaseErr)
	}
	if err := <-checkpointResult; err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	assertRunResult(t, path, result.RunID, "deferred", "busy")
	assertRunReclaimedBytes(t, path, result.RunID, 0)
}

func TestCompactTrailingCheckpointContentionKeepsCommittedRunSuccessful(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		symlink  bool
		relative bool
	}{
		{name: "direct"},
		{name: "absolute_symlink", symlink: true},
		{name: "relative_symlink", symlink: true, relative: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			targetPath := createCompactFixture(t, 1<<20)
			configuredPath := targetPath
			if testCase.symlink {
				configuredPath = createCompactSymlink(t, targetPath, testCase.relative)
			}
			beforePages, beforeFree := compactPageState(t, targetPath)
			reader := openCompactDatabase(t, targetPath)
			readerConnection, err := reader.Conn(t.Context())
			if err != nil {
				t.Fatalf("reserve trailing checkpoint reader: %v", err)
			}
			defer func() { _ = readerConnection.Close() }()
			readerActive := false
			defer func() {
				if readerActive {
					_, _ = readerConnection.ExecContext(t.Context(), `rollback`)
				}
			}()
			var startErr error
			checkpointBlocked := false
			log := slog.New(compactTrailingCheckpointHandler{
				start: func() {
					if _, startErr = readerConnection.ExecContext(t.Context(), `begin`); startErr != nil {
						return
					}
					readerActive = true
					var rows int
					startErr = readerConnection.QueryRowContext(
						t.Context(), `select count(*) from intake_events`,
					).Scan(&rows)
				},
				failed: func() { checkpointBlocked = true },
			})
			options := compactApplyOptions(configuredPath, 100)
			options.Log = log

			result, err := Compact(t.Context(), options)
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			afterPages, afterFree := compactPageState(t, targetPath)
			if startErr != nil {
				t.Fatalf("start trailing checkpoint reader: %v", startErr)
			}
			if !readerActive || !checkpointBlocked {
				t.Fatalf(
					"reader active/checkpoint blocked = %t/%t, want true/true",
					readerActive,
					checkpointBlocked,
				)
			}
			if afterPages >= beforePages || afterFree >= beforeFree {
				t.Fatalf(
					"page/free counts = %d/%d after %d/%d, want committed reclamation",
					afterPages,
					afterFree,
					beforePages,
					beforeFree,
				)
			}
			if result.Result != "success" || result.Err != nil || result.NextDueAt == nil {
				t.Fatalf("result = %#v, want successful committed compaction", result)
			}
			if result.ReclaimedBytes != 0 {
				t.Fatalf(
					"reclaimed bytes = %d, want 0 after blocked trailing checkpoint",
					result.ReclaimedBytes,
				)
			}
			assertRunResult(t, targetPath, result.RunID, "success", "")
			assertRunReclaimedBytes(t, targetPath, result.RunID, 0)
			assertRunNextDue(t, targetPath, result.RunID)
			if testCase.symlink {
				assertNoCompactSidecars(t, configuredPath)
			}
		})
	}
}

func TestCompactMeasuresCanonicalMainBytesThroughDatabaseSymlink(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		relative bool
	}{
		{name: "absolute"},
		{name: "relative", relative: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			targetPath := createCompactFixture(t, 1<<20)
			aliasPath := createCompactSymlink(t, targetPath, testCase.relative)
			keeper := openCompactDatabase(t, targetPath)
			keeper.SetMaxOpenConns(1)
			if _, err := keeper.ExecContext(t.Context(), `pragma wal_autocheckpoint = 0`); err != nil {
				t.Fatalf("keep target WAL open: %v", err)
			}
			beforeMain := compactMainBytes(t, targetPath)

			result, err := Compact(t.Context(), compactApplyOptions(aliasPath, 100))
			if err != nil {
				t.Fatalf("Compact through symlink: %v", err)
			}
			if result.Result != "success" || result.Err != nil || result.NextDueAt == nil {
				t.Fatalf("result = %#v, want successful compaction", result)
			}
			afterMain := compactMainBytes(t, targetPath)
			wantReclaimed := max(beforeMain-afterMain, 0)
			if result.ReclaimedBytes != wantReclaimed {
				t.Fatalf(
					"reclaimed bytes = %d, want canonical main database reduction %d; main before/after = %d/%d",
					result.ReclaimedBytes,
					wantReclaimed,
					beforeMain,
					afterMain,
				)
			}
			walInfo, err := os.Stat(targetPath + "-wal")
			if err != nil {
				t.Fatalf("stat retained target WAL: %v", err)
			}
			if walInfo.Size() == 0 {
				t.Fatal("retained target WAL is empty")
			}
			assertRunResult(t, targetPath, result.RunID, "success", "")
			assertRunReclaimedBytes(t, targetPath, result.RunID, wantReclaimed)
			assertRunNextDue(t, targetPath, result.RunID)
			assertNoCompactSidecars(t, aliasPath)
		})
	}
}

func TestCompactCancellationAfterVacuumCommitKeepsRunSuccessful(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	database := openCompactApplyDatabase(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	handler := &compactCommitCancellationHandler{database: database, ctx: t.Context(), cancel: cancel}
	options := compactApplyOptions(path, 100)
	options.Log = slog.New(handler)
	beforePages, beforeFree := compactPageState(t, path)

	result, err := compactWithDatabase(ctx, database, options)
	afterPages, afterFree := compactPageState(t, path)
	if handler.installErr != nil || !handler.committed.Load() {
		t.Fatalf("commit hook error/triggered = %v/%t, want nil/true", handler.installErr, handler.committed.Load())
	}
	if afterPages >= beforePages || afterFree >= beforeFree {
		t.Fatalf(
			"page/free counts = %d/%d after %d/%d, want committed reclamation",
			afterPages,
			afterFree,
			beforePages,
			beforeFree,
		)
	}
	if err != nil || result.Result != "success" || result.ErrorClass != "" || result.NextDueAt == nil {
		t.Fatalf("result/error = %#v/%v, want successful committed compaction", result, err)
	}
	if result.ReclaimedBytes != 0 {
		t.Fatalf("reclaimed bytes = %d, want 0 after cancelled trailing checkpoint", result.ReclaimedBytes)
	}
	assertRunResult(t, path, result.RunID, "success", "")
	assertRunReclaimedBytes(t, path, result.RunID, 0)
	assertRunNextDue(t, path, result.RunID)
}

func TestCompactAfterApplyCancellationAfterVacuumCommitKeepsRunSuccessful(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	database := openCompactApplyDatabase(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	handler := &compactCommitCancellationHandler{database: database, ctx: t.Context(), cancel: cancel}
	options := compactApplyOptions(path, 100)
	options.Log = slog.New(handler)
	plan, err := initialRunPlan(options.Policy, options.Now)
	if err != nil {
		t.Fatalf("initial run plan: %v", err)
	}
	runID, err := newRunID()
	if err != nil {
		t.Fatalf("new run ID: %v", err)
	}
	result := Result{
		RunID: runID, Plan: plan, CompactPlan: emptyCompactPlan(),
		SizeState: SizeStateDisabled, Result: "running",
	}
	lease := maintenanceLease{owner: options.Owner, runID: runID, ttl: options.LeaseTTL}
	if err := acquireLease(ctx, database, lease, options.Now); err != nil {
		t.Fatalf("acquire normal apply lease: %v", err)
	}
	defer func() {
		if err := releaseLeaseBounded(t.Context(), database, lease); err != nil {
			t.Errorf("release normal apply lease: %v", err)
		}
	}()
	if err := recordRunStartLogged(ctx, database, result, options.Now, options.Log); err != nil {
		t.Fatalf("record normal apply start: %v", err)
	}
	beforePages, beforeFree := compactPageState(t, path)

	applyErr := compactAfterApply(ctx, database, lease, options, &result)
	result, err = finishApply(ctx, database, options, result, applyErr)
	afterPages, afterFree := compactPageState(t, path)
	if handler.installErr != nil || !handler.committed.Load() {
		t.Fatalf("commit hook error/triggered = %v/%t, want nil/true", handler.installErr, handler.committed.Load())
	}
	if afterPages >= beforePages || afterFree >= beforeFree {
		t.Fatalf(
			"page/free counts = %d/%d after %d/%d, want committed reclamation",
			afterPages,
			afterFree,
			beforePages,
			beforeFree,
		)
	}
	if err != nil || result.Result != "success" || result.ErrorClass != "" || result.NextDueAt == nil {
		t.Fatalf("result/error = %#v/%v, want successful normal apply", result, err)
	}
	if result.ReclaimedBytes != 0 {
		t.Fatalf("reclaimed bytes = %d, want 0 after cancelled trailing checkpoint", result.ReclaimedBytes)
	}
	assertRunResult(t, path, result.RunID, "success", "")
	assertRunReclaimedBytes(t, path, result.RunID, 0)
	assertRunNextDue(t, path, result.RunID)
}

func TestCompactCancellationBeforeVacuumFailsWithoutReclamation(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	database := openCompactApplyDatabase(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	options := compactApplyOptions(path, 100)
	options.Log = slog.New(compactTrailingCheckpointHandler{
		start:  cancel,
		failed: func() {},
	})
	beforePages, beforeFree := compactPageState(t, path)

	result, err := compactWithDatabase(ctx, database, options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact error = %v, want context canceled", err)
	}
	afterPages, afterFree := compactPageState(t, path)
	if afterPages != beforePages || afterFree != beforeFree {
		t.Fatalf(
			"page/free counts = %d/%d after %d/%d, want no reclamation",
			afterPages,
			afterFree,
			beforePages,
			beforeFree,
		)
	}
	if result.Result != "failed" || result.ErrorClass != "cancelled" || result.NextDueAt != nil {
		t.Fatalf("result = %#v, want failed cancelled without next due", result)
	}
	assertRunResult(t, path, result.RunID, "failed", "cancelled")
	assertRunReclaimedBytes(t, path, result.RunID, 0)
	assertRunNextDueAbsent(t, path, result.RunID)
}

func TestCompactCancellationDoesNotReclaimPages(t *testing.T) {
	path := createCompactFixture(t, 1<<20)
	_, beforeFree := compactPageState(t, path)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := Compact(ctx, compactApplyOptions(path, 100))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Compact error = %v, want context canceled", err)
	}
	_, afterFree := compactPageState(t, path)
	if afterFree != beforeFree {
		t.Fatalf("free pages changed from %d to %d after cancellation", beforeFree, afterFree)
	}
}

func TestCompactApplyIsIdempotentAfterFreePagesExhausted(t *testing.T) {
	path := createCompactFixture(t, 64<<10)
	options := compactApplyOptions(path, 1000)
	for attempt := 0; attempt < 100; attempt++ {
		result, err := Compact(t.Context(), options)
		if err != nil {
			t.Fatalf("Compact attempt %d: %v", attempt, err)
		}
		if result.CompactPlan.FreePages == 0 {
			break
		}
	}
	result, err := Compact(t.Context(), options)
	if err != nil {
		t.Fatalf("idempotent Compact: %v", err)
	}
	if result.ReclaimedBytes != 0 || result.CompactPlan.FreePages != 0 {
		t.Fatalf("idempotent result = %#v, want no remaining or reclaimed pages", result)
	}
}

func createCompactFixture(t *testing.T, paddingBytes int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "retained", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: []byte(`{}`), NormalizedJSON: []byte(`{}`),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("Append retained row: %v", err)
	}
	createCompactFreePages(t, store.Handle(), paddingBytes)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func createCompactSymlink(t *testing.T, targetPath string, relative bool) string {
	t.Helper()
	aliasDirectory := t.TempDir()
	linkTarget := targetPath
	if relative {
		var err error
		linkTarget, err = filepath.Rel(aliasDirectory, targetPath)
		if err != nil {
			t.Fatalf("resolve relative symlink target: %v", err)
		}
	}
	aliasPath := filepath.Join(aliasDirectory, "audit.db")
	if err := os.Symlink(linkTarget, aliasPath); err != nil {
		t.Fatalf("create audit database symlink: %v", err)
	}
	return aliasPath
}

func createCompactMaintenanceFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(t.Context(), path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "expired", RecordedAt: old, System: "codex", SessionID: "session",
		EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: bytes.Repeat([]byte("x"), 1<<20), NormalizedJSON: []byte(`{}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append expired row: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `
		update intake_receipts set received_at = ? where receipt_id = ?;
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		) values ('evaluation-expired', ?, 'expired', 1, 'hot', 'config', 'version',
			'commit', 'build', 'input', ?, ?, 'allow', 'deterministic', 'allow',
			0, 1, 0, 0, 'available');
		pragma wal_checkpoint(truncate);
	`, old.Format(time.RFC3339Nano), receipt.ReceiptID, receipt.ReceiptID,
		old.Format(time.RFC3339Nano), old.Format(time.RFC3339Nano)); err != nil {
		_ = store.Close()
		t.Fatalf("complete expired graph: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path
}

func createLegacyCompactFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	database := openCompactDatabase(t, path)
	if _, err := database.ExecContext(t.Context(), `
		pragma auto_vacuum = none;
		create table retained(value text not null);
		insert into retained values ('keep');
		create table padding(value blob not null);
		insert into padding values (zeroblob(1048576));
		drop table padding;
	`); err != nil {
		t.Fatalf("create legacy fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy fixture: %v", err)
	}
	return path
}

func createCompactFreePages(t *testing.T, database *sql.DB, paddingBytes int) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
		pragma wal_checkpoint(truncate);
		create table compact_padding(value blob not null);
		insert into compact_padding values (zeroblob(?));
		pragma wal_checkpoint(truncate);
		drop table compact_padding;
		pragma wal_checkpoint(truncate);
	`, paddingBytes); err != nil {
		t.Fatalf("create compact free pages: %v", err)
	}
}

func compactApplyOptions(path string, batchRows int) ApplyOptions {
	policy := config.AuditStoragePolicy{
		Profile: config.AuditStorageProfileBalanced, MaintenanceInterval: 24 * time.Hour,
		MaxSizeBytes: 0, MaintenanceBatchRows: batchRows, CompactAfterMaintenance: true,
		FullDetailRetention: 7 * 24 * time.Hour, SummaryRetention: 30 * 24 * time.Hour,
		Detail: config.AuditStorageDetailPolicy{
			WireInput: true, NormalizedInput: true, ProviderEvidence: true,
			EnvironmentEvidence: true, EvaluationContent: true,
		},
	}
	return ApplyOptions{
		Path: path, Policy: policy, Now: time.Now().UTC(), Owner: "compact-test",
		LeaseTTL: time.Minute, Log: nil,
	}
}

func compactPageState(t *testing.T, path string) (int64, int64) {
	t.Helper()
	database := openCompactDatabase(t, path)
	var pages int64
	var free int64
	if err := database.QueryRowContext(t.Context(), `pragma page_count`).Scan(&pages); err != nil {
		t.Fatalf("read page count: %v", err)
	}
	if err := database.QueryRowContext(t.Context(), `pragma freelist_count`).Scan(&free); err != nil {
		t.Fatalf("read free pages: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close page state database: %v", err)
	}
	return pages, free
}

func openCompactDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open SQLite: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func openCompactApplyDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := openApplyDatabase(t.Context(), path)
	if err != nil {
		t.Fatalf("open compact apply database: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func assertCompactIntegrity(t *testing.T, database *sql.DB) {
	t.Helper()
	var result string
	if err := database.QueryRowContext(t.Context(), `pragma quick_check`).Scan(&result); err != nil {
		t.Fatalf("quick_check: %v", err)
	}
	if result != "ok" {
		t.Fatalf("quick_check = %q, want ok", result)
	}
}

func assertRunReclaimedBytes(t *testing.T, path string, runID string, want int64) {
	t.Helper()
	database := openCompactDatabase(t, path)
	var got int64
	if err := database.QueryRowContext(t.Context(), `
		select reclaimed_bytes from audit_maintenance_runs where run_id = ?
	`, runID).Scan(&got); err != nil {
		t.Fatalf("read run reclaimed bytes: %v", err)
	}
	if got != want {
		t.Fatalf("run reclaimed bytes = %d, want %d", got, want)
	}
}

func assertRunResult(t *testing.T, path string, runID string, wantResult string, wantClass string) {
	t.Helper()
	database := openCompactDatabase(t, path)
	var result string
	var class string
	if err := database.QueryRowContext(t.Context(), `
		select result, error_class from audit_maintenance_runs where run_id = ?
	`, runID).Scan(&result, &class); err != nil {
		t.Fatalf("read compact run result: %v", err)
	}
	if result != wantResult || class != wantClass {
		t.Fatalf("run result/class = %q/%q, want %q/%q", result, class, wantResult, wantClass)
	}
}

func assertRunNextDue(t *testing.T, path string, runID string) {
	t.Helper()
	database := openCompactDatabase(t, path)
	var nextDue sql.NullString
	if err := database.QueryRowContext(t.Context(), `
		select next_due_at from audit_maintenance_runs where run_id = ?
	`, runID).Scan(&nextDue); err != nil {
		t.Fatalf("read compact run next due: %v", err)
	}
	if !nextDue.Valid || nextDue.String == "" {
		t.Fatal("compact run next due is empty")
	}
}

func assertRunNextDueAbsent(t *testing.T, path string, runID string) {
	t.Helper()
	database := openCompactDatabase(t, path)
	var nextDue sql.NullString
	if err := database.QueryRowContext(t.Context(), `
		select next_due_at from audit_maintenance_runs where run_id = ?
	`, runID).Scan(&nextDue); err != nil {
		t.Fatalf("read compact run next due: %v", err)
	}
	if nextDue.Valid {
		t.Fatalf("compact run next due = %q, want null", nextDue.String)
	}
}

func TestMeasureDatabaseMainBytesDoesNotCreateWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database := openCompactDatabase(t, path)
	if _, err := database.ExecContext(t.Context(), `
		pragma journal_mode = delete;
		create table retained(value text not null);
		insert into retained values ('keep');
	`); err != nil {
		t.Fatalf("create database without WAL: %v", err)
	}
	assertNoCompactSidecars(t, path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat database without WAL: %v", err)
	}

	got, err := measureDatabaseMainBytes(t.Context(), database)
	if err != nil {
		t.Fatalf("measure database without WAL: %v", err)
	}
	if got != info.Size() {
		t.Fatalf("main database bytes = %d, want %d", got, info.Size())
	}
	assertNoCompactSidecars(t, path)
}

func TestMeasureDatabaseMainBytesIgnoresResolvedWAL(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		relative bool
	}{
		{name: "absolute"},
		{name: "relative", relative: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			targetPath := createCompactFixture(t, 1<<20)
			aliasPath := createCompactSymlink(t, targetPath, testCase.relative)
			database := openCompactDatabase(t, aliasPath)
			if _, err := database.ExecContext(t.Context(), `
				pragma wal_autocheckpoint = 0;
				update intake_events set command = command || 'x' where event_id = 'retained';
			`); err != nil {
				t.Fatalf("write target WAL through symlink: %v", err)
			}
			walInfo, err := os.Stat(targetPath + "-wal")
			if err != nil {
				t.Fatalf("stat resolved WAL: %v", err)
			}
			if walInfo.Size() == 0 {
				t.Fatal("resolved WAL is empty")
			}

			got, err := measureDatabaseMainBytes(t.Context(), database)
			if err != nil {
				t.Fatalf("measure database through symlink: %v", err)
			}
			want := compactMainBytes(t, targetPath)
			if got != want {
				t.Fatalf("reclaim metric bytes = %d, want canonical main database bytes %d", got, want)
			}
			assertNoCompactSidecars(t, aliasPath)
		})
	}
}

func TestMeasureDatabaseMainBytesReturnsPathQueryError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.db")
	database := openCompactDatabase(t, path)
	if _, err := database.ExecContext(t.Context(), `create table retained(value text not null)`); err != nil {
		t.Fatalf("create database: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close database before measurement: %v", err)
	}

	if _, err := measureDatabaseMainBytes(t.Context(), database); err == nil {
		t.Fatal("measure closed database succeeded, want path query error")
	}
}

func compactMainBytes(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat main database %s: %v", path, err)
	}
	return info.Size()
}

func assertNoCompactSidecars(t *testing.T, path string) {
	t.Helper()
	for _, sidecarPath := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Lstat(sidecarPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat symlink sidecar %s: %v, want not exist", sidecarPath, err)
		}
	}
}

func waitForCompactCheckpointContention(t *testing.T, path string) {
	t.Helper()
	probe := openCompactDatabase(t, path)
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

type compactCheckpointHandler struct {
	start   func()
	release func()
}

type compactCommitCancellationHandler struct {
	database   *sql.DB
	ctx        context.Context
	cancel     context.CancelFunc
	installed  bool
	installErr error
	committed  atomic.Bool
}

func (handler *compactCommitCancellationHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler *compactCommitCancellationHandler) Handle(
	_ context.Context,
	record slog.Record,
) error {
	if record.Message != logCheckpointCompact || handler.installed {
		return nil
	}
	handler.installed = true
	connection, err := handler.database.Conn(handler.ctx)
	if err != nil {
		handler.installErr = err
		return nil
	}
	defer func() {
		if err := connection.Close(); err != nil && handler.installErr == nil {
			handler.installErr = err
		}
	}()
	handler.installErr = connection.Raw(func(driverConnection any) error {
		sqliteConnection, ok := driverConnection.(*sqlite3.SQLiteConn)
		if !ok {
			return errors.New("compact connection is not SQLite")
		}
		sqliteConnection.RegisterCommitHook(func() int {
			handler.committed.Store(true)
			handler.cancel()
			return 0
		})
		return nil
	})
	return nil
}

func (handler *compactCommitCancellationHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler *compactCommitCancellationHandler) WithGroup(string) slog.Handler {
	return handler
}

type compactTrailingCheckpointHandler struct {
	start  func()
	failed func()
}

func (handler compactTrailingCheckpointHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler compactTrailingCheckpointHandler) Handle(
	_ context.Context,
	record slog.Record,
) error {
	if record.Message == logCheckpointCompact {
		handler.start()
	}
	if record.Message == logCompactCheckpointFailed {
		handler.failed()
	}
	return nil
}

func (handler compactTrailingCheckpointHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler compactTrailingCheckpointHandler) WithGroup(string) slog.Handler {
	return handler
}

func (handler compactCheckpointHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (handler compactCheckpointHandler) Handle(_ context.Context, record slog.Record) error {
	if record.Message == logCheckpointCompact {
		handler.start()
	}
	if record.Message == logCompactCheckpointFailed {
		handler.release()
	}
	return nil
}

func (handler compactCheckpointHandler) WithAttrs([]slog.Attr) slog.Handler {
	return handler
}

func (handler compactCheckpointHandler) WithGroup(string) slog.Handler {
	return handler
}

func snapshotCompactFiles(t *testing.T, path string) map[string][32]byte {
	t.Helper()
	result := make(map[string][32]byte)
	for _, current := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(current)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatalf("ReadFile %s: %v", current, err)
		}
		result[current] = sha256.Sum256(body)
	}
	return result
}
