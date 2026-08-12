package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/agent-gate/internal/auditstorage"
)

const (
	autoVacuumIncremental      = 2
	logCheckpointCompact       = "checkpoint audit database before incremental compaction"
	logCompactCheckpointFailed = "audit database compaction checkpoint failed"
)

// CompactPlan describes one bounded incremental page-reclamation attempt.
type CompactPlan struct {
	AutoVacuumMode   int   `json:"auto_vacuum_mode"`
	FreePages        int64 `json:"free_pages"`
	PagesToReclaim   int64 `json:"pages_to_reclaim"`
	FullModeRequired bool  `json:"full_mode_required"`
}

func emptyCompactPlan() CompactPlan {
	return CompactPlan{
		AutoVacuumMode: 0, FreePages: 0, PagesToReclaim: 0, FullModeRequired: false,
	}
}

func compactAfterApply(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	options ApplyOptions,
	result *Result,
) error {
	if !options.Policy.CompactAfterMaintenance {
		return nil
	}
	plan, err := readCompactPlan(ctx, database, options.Policy.MaintenanceBatchRows)
	if err != nil {
		return err
	}
	result.CompactPlan = plan
	if plan.FullModeRequired {
		return nil
	}
	result.ReclaimedBytes, err = applyCompactPlan(
		ctx,
		database,
		lease,
		plan,
		options.Log,
	)
	return err
}

// PreviewCompact reports bounded page reclamation without changing the source database.
func PreviewCompact(ctx context.Context, path string, batchRows int) (CompactPlan, error) {
	if strings.TrimSpace(path) == "" {
		return CompactPlan{}, errors.New("audit database path is required")
	}
	if batchRows <= 0 {
		return CompactPlan{}, errors.New("audit maintenance batch size must be positive")
	}
	snapshot, err := openDatabaseSnapshot(ctx, path)
	if err != nil {
		return CompactPlan{}, err
	}
	defer snapshot.cleanup()
	return readCompactPlan(ctx, snapshot.database, batchRows)
}

// Compact checkpoints and reclaims one bounded set of free pages.
func Compact(ctx context.Context, options ApplyOptions) (result Result, returnErr error) {
	if options.Log == nil {
		options.Log = slog.Default()
	}
	if err := validateApplyOptions(options); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return unknownResult(), wrapError("start audit compaction", err)
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
		if closeErr := database.Close(); closeErr != nil {
			returnErr = errors.Join(
				returnErr,
				wrapError("close audit compaction database", closeErr),
			)
		}
	}()
	if err := auditstorage.MigrateNonblocking(ctx, database); err != nil {
		migrationErr := classifyMaintenanceWriteError("migrate audit compaction database", err)
		if errors.Is(migrationErr, ErrMaintenanceBusy) {
			return deferredResult(migrationErr), nil
		}
		return Result{}, migrationErr
	}
	return compactWithDatabase(ctx, database, options)
}

func compactWithDatabase(
	ctx context.Context,
	database *sql.DB,
	options ApplyOptions,
) (result Result, returnErr error) {
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
		SizeState: SizeStateDisabled, Result: "running", ErrorClass: "",
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
	result.CompactPlan, err = readCompactPlan(
		ctx,
		database,
		options.Policy.MaintenanceBatchRows,
	)
	if err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}
	if err := recordRunStartLogged(ctx, database, result, startedAt, options.Log); err != nil {
		return finishUnstartedApply(ctx, database, options, result, err)
	}
	if !result.CompactPlan.FullModeRequired {
		result.ReclaimedBytes, err = applyCompactPlan(
			ctx,
			database,
			lease,
			result.CompactPlan,
			options.Log,
		)
	}
	return finishApply(ctx, database, options, result, err)
}

func readCompactPlan(
	ctx context.Context,
	database *sql.DB,
	batchRows int,
) (CompactPlan, error) {
	plan := CompactPlan{
		AutoVacuumMode: 0, FreePages: 0, PagesToReclaim: 0, FullModeRequired: false,
	}
	if err := database.QueryRowContext(ctx, `pragma auto_vacuum`).Scan(
		&plan.AutoVacuumMode,
	); err != nil {
		return CompactPlan{}, wrapError("read audit auto-vacuum mode", err)
	}
	if err := database.QueryRowContext(ctx, `pragma freelist_count`).Scan(
		&plan.FreePages,
	); err != nil {
		return CompactPlan{}, wrapError("read audit free page count", err)
	}
	plan.FullModeRequired = plan.AutoVacuumMode != autoVacuumIncremental
	if !plan.FullModeRequired {
		plan.PagesToReclaim = min(plan.FreePages, int64(batchRows))
	}
	return plan, nil
}

func applyCompactPlan(
	ctx context.Context,
	database *sql.DB,
	lease maintenanceLease,
	plan CompactPlan,
	log *slog.Logger,
) (int64, error) {
	if err := lease.renew(ctx, database, maintenanceNow().UTC()); err != nil {
		return 0, err
	}
	log.DebugContext(ctx, logCheckpointCompact)
	if err := checkpointCompactDatabase(ctx, database); err != nil {
		log.DebugContext(ctx, logCompactCheckpointFailed, "err", err)
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, wrapError("reclaim audit database pages", err)
	}
	if plan.PagesToReclaim == 0 {
		return 0, nil
	}
	beforeBytes, err := measureDatabaseMainBytes(ctx, database)
	if err != nil {
		return 0, err
	}
	statement := fmt.Sprintf("pragma incremental_vacuum(%d)", plan.PagesToReclaim)
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return 0, classifyMaintenanceWriteError("reclaim audit database pages", err)
	}
	if err := checkpointCompactDatabase(ctx, database); err != nil {
		if errors.Is(err, ErrMaintenanceBusy) ||
			errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			log.DebugContext(ctx, logCompactCheckpointFailed, "err", err)
			return 0, nil
		}
		return 0, err
	}
	afterBytes, err := measureDatabaseMainBytes(ctx, database)
	if err != nil {
		return 0, err
	}
	reclaimedBytes := max(beforeBytes-afterBytes, 0)
	return reclaimedBytes, nil
}

// Reclaimed bytes track canonical main-file shrink. Later ledger writes can change WAL size.
func measureDatabaseMainBytes(ctx context.Context, database *sql.DB) (int64, error) {
	var path string
	if err := database.QueryRowContext(
		ctx,
		`select file from pragma_database_list where name = 'main'`,
	).Scan(&path); err != nil {
		return 0, wrapError("resolve audit database path", err)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return 0, wrapError("resolve audit database path", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, wrapError("measure audit main database bytes", err)
	}
	return info.Size(), nil
}

func checkpointCompactDatabase(ctx context.Context, database *sql.DB) error {
	var busy int64
	var frames int64
	var checkpointed int64
	if err := database.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&busy,
		&frames,
		&checkpointed,
	); err != nil {
		return classifyMaintenanceWriteError("checkpoint audit database before compaction", err)
	}
	if busy != 0 || frames < 0 || checkpointed < 0 || checkpointed < frames {
		return ErrMaintenanceBusy
	}
	return nil
}
