package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/processlock"
)

const fullCompactLeaseCleanupTimeout = 5 * time.Second

// FullCompactApplyOptions configures one explicit offline full compaction.
type FullCompactApplyOptions struct {
	Path             string
	RuntimeDirectory string
	Owner            string
	LeaseTTL         time.Duration
	InspectService   func(context.Context) (installer.ServiceState, error)
	FreeBytes        func(string) (uint64, error)
	FailStep         func(string) error
	Now              func() time.Time
}

// FullCompactResult reports one committed replacement.
type FullCompactResult struct {
	RunID          string `json:"run_id"`
	BeforeBytes    int64  `json:"before_bytes"`
	AfterBytes     int64  `json:"after_bytes"`
	ReclaimedBytes int64  `json:"reclaimed_bytes"`
}

// ApplyFullCompact installs a verified compact copy while the managed daemon is stopped.
func ApplyFullCompact(
	ctx context.Context,
	options FullCompactApplyOptions,
) (result FullCompactResult, returnErr error) {
	slog.DebugContext(ctx, "apply new full audit compaction", "path", options.Path)
	slog.DebugContext(ctx, "apply full audit compaction")
	if err := validateFullCompactApplyOptions(options); err != nil {
		return FullCompactResult{}, err
	}
	service, err := options.InspectService(ctx)
	if err != nil {
		return FullCompactResult{}, reportFullCompactError("inspect managed service before full compaction", err)
	}
	if err := requireStoppedFullCompactService(service); err != nil {
		return FullCompactResult{}, err
	}
	fileLock, err := acquireFileLock(options.Path)
	if err != nil {
		return FullCompactResult{}, err
	}
	defer func() { returnErr = errors.Join(returnErr, fileLock.release()) }()
	if err := os.MkdirAll(options.RuntimeDirectory, 0o700); err != nil {
		return FullCompactResult{}, wrapError("create daemon runtime directory", err)
	}
	if journal, exists, readErr := auditstorage.ReadCutoverJournal(options.Path); readErr != nil {
		return FullCompactResult{}, wrapError("read full compaction journal", readErr)
	} else if exists {
		processLock, lockErr := processlock.Acquire(options.RuntimeDirectory)
		if lockErr != nil {
			return FullCompactResult{}, reportFullCompactError(
				"acquire daemon process lock for full compaction recovery",
				lockErr,
			)
		}
		defer func() { returnErr = errors.Join(returnErr, processLock.Release()) }()
		if err := requireStoppedInspection(ctx, options); err != nil {
			return FullCompactResult{}, reportFullCompactError("verify stopped service before recovery", err)
		}
		return recoverFullCompact(ctx, options, journal)
	}
	return applyNewFullCompact(ctx, options, service)
}

func applyNewFullCompact(
	ctx context.Context,
	options FullCompactApplyOptions,
	service installer.ServiceState,
) (result FullCompactResult, returnErr error) {
	slog.DebugContext(ctx, "apply new full audit compaction", "path", options.Path)
	plan, err := PreviewFullCompact(ctx, FullCompactOptions{
		Path: options.Path, Service: service, FreeBytes: options.FreeBytes,
	})
	if err != nil {
		return FullCompactResult{}, err
	}
	runID, err := newRunID()
	if err != nil {
		return FullCompactResult{}, err
	}
	workingPath := plan.DatabasePath + ".compact." + runID + ".working"
	rollbackPath := plan.DatabasePath + ".compact." + runID + ".rollback"
	failedPath := plan.DatabasePath + ".compact." + runID + ".failed"
	defer func() {
		if returnErr != nil {
			returnErr = errors.Join(
				returnErr,
				preserveWorkingFullCompactFailure(workingPath, failedPath, plan.DatabasePath),
			)
		}
	}()
	lease := maintenanceLease{owner: options.Owner, runID: runID, ttl: options.LeaseTTL}
	database, err := openFullCompactDatabase(ctx, plan.DatabasePath)
	if err != nil {
		return FullCompactResult{}, err
	}
	leaseHeld := false
	defer func() {
		if database != nil {
			if leaseHeld {
				returnErr = errors.Join(returnErr, releaseLeaseBounded(ctx, database, lease))
				leaseHeld = false
			}
			_ = database.Close()
		}
		if leaseHeld {
			returnErr = errors.Join(
				returnErr,
				releaseBoundFullCompactLease(ctx, plan.DatabasePath, lease, plan.identity),
			)
		}
	}()
	if err := acquireLease(ctx, database, lease, options.Now().UTC()); err != nil {
		return FullCompactResult{}, err
	}
	leaseHeld = true
	processLock, err := processlock.Acquire(options.RuntimeDirectory)
	if err != nil {
		return FullCompactResult{}, reportFullCompactError("acquire daemon process lock for full compaction", err)
	}
	defer func() { returnErr = errors.Join(returnErr, processLock.Release()) }()
	if err := requireStoppedInspection(ctx, options); err != nil {
		return FullCompactResult{}, err
	}
	if err := validateFullCompactIdentity(plan.DatabasePath, plan.identity); err != nil {
		return FullCompactResult{}, err
	}
	sourceMode, err := buildFullCompactCopy(ctx, database, workingPath, options.FailStep)
	if err != nil {
		return FullCompactResult{}, err
	}
	if err := releaseFullCompactSourceAndCopyLeases(ctx, database, workingPath, lease); err != nil {
		return FullCompactResult{}, err
	}
	leaseHeld = false
	if err := database.Close(); err != nil {
		database = nil
		return FullCompactResult{}, wrapError("close full compaction source", err)
	}
	database = nil
	if err := finalizeFullCompactCopy(ctx, plan.DatabasePath, workingPath, sourceMode); err != nil {
		return FullCompactResult{}, err
	}
	if err := failFullCompactStep(options, "copy-verified"); err != nil {
		return FullCompactResult{}, err
	}
	sourceIdentity, err := readBoundFullCompactIdentity(plan.DatabasePath, plan.identity)
	if err != nil {
		return FullCompactResult{}, err
	}
	journal, err := prepareFullCompactJournal(
		plan, service, runID, workingPath, rollbackPath, failedPath, sourceIdentity,
	)
	if err != nil {
		return FullCompactResult{}, err
	}
	return executeFullCompactCutover(ctx, options, plan, journal, journal.CopyIdentity)
}

func prepareFullCompactJournal(
	plan FullCompactPlan,
	service installer.ServiceState,
	runID string,
	workingPath string,
	rollbackPath string,
	failedPath string,
	sourceIdentity auditstorage.FileIdentity,
) (auditstorage.CutoverJournal, error) {
	copyIdentity, err := fullCompactFileIdentity(workingPath)
	if err != nil {
		return auditstorage.CutoverJournal{}, err
	}
	walIdentity, err := optionalFullCompactFileIdentity(plan.DatabasePath + "-wal")
	if err != nil {
		return auditstorage.CutoverJournal{}, err
	}
	shmIdentity, err := optionalFullCompactFileIdentity(plan.DatabasePath + "-shm")
	if err != nil {
		return auditstorage.CutoverJournal{}, err
	}
	return auditstorage.CutoverJournal{
		Version: 1, RunID: runID, DatabasePath: plan.DatabasePath,
		WorkingPath: workingPath, RollbackPath: rollbackPath, FailedPath: failedPath,
		ServicePlatform: service.Platform, ServiceBinary: service.BinaryPath,
		ServiceRunning: service.Running,
		SourceIdentity: sourceIdentity, CopyIdentity: copyIdentity,
		WALIdentity: walIdentity, SHMIdentity: shmIdentity,
		ReplacementAccessAuthorized: false,
		Phase:                       auditstorage.CutoverPrepared,
	}, nil
}

func executeFullCompactCutover(
	ctx context.Context,
	options FullCompactApplyOptions,
	plan FullCompactPlan,
	journal auditstorage.CutoverJournal,
	copyIdentity auditstorage.FileIdentity,
) (FullCompactResult, error) {
	slog.DebugContext(ctx, "execute full audit compaction cutover", "run_id", journal.RunID)
	if err := requireStoppedInspection(ctx, options); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if matches, err := sameFullCompactIdentity(plan.DatabasePath, journal.SourceIdentity); err != nil || !matches {
		return FullCompactResult{}, errors.New("audit database path changed before full compaction journal creation")
	}
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverPrepared, options.FailStep,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := requireStoppedInspection(ctx, options); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverOriginalRenaming, options.FailStep,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := failFullCompactHook(options.FailStep, "before-rename:original"); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := os.Rename(plan.DatabasePath, journal.RollbackPath); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, wrapError("rename original audit database", err))
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(plan.DatabasePath)); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := verifyRenamedFullCompactOriginal(journal); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := failFullCompactHook(options.FailStep, "after-rename:original"); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverOriginalRenamed, options.FailStep,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := moveFullCompactSidecar(
		&journal, options, "-wal", journal.WALIdentity,
		auditstorage.CutoverWALMoving, auditstorage.CutoverWALMoved,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := moveFullCompactSidecar(
		&journal, options, "-shm", journal.SHMIdentity,
		auditstorage.CutoverSHMMoving, auditstorage.CutoverSHMMoved,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverInstalling, options.FailStep,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := failFullCompactHook(options.FailStep, "before-rename:replacement"); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := os.Rename(journal.WorkingPath, plan.DatabasePath); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, wrapError("install compact audit database", err))
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(plan.DatabasePath)); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := failFullCompactHook(options.FailStep, "after-rename:replacement"); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverInstalled, options.FailStep,
	); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := verifyInstalledFullCompact(ctx, journal, true); err != nil {
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	return commitFullCompactCutover(ctx, options, plan, journal, copyIdentity)
}

func verifyRenamedFullCompactOriginal(journal auditstorage.CutoverJournal) error {
	matches, err := sameFullCompactIdentity(journal.RollbackPath, journal.SourceIdentity)
	if err != nil {
		return wrapError("verify renamed original audit database", err)
	}
	if !matches {
		return errors.New("renamed original audit database identity does not match journal")
	}
	return nil
}

func commitFullCompactCutover(
	ctx context.Context,
	options FullCompactApplyOptions,
	plan FullCompactPlan,
	journal auditstorage.CutoverJournal,
	copyIdentity auditstorage.FileIdentity,
) (FullCompactResult, error) {
	slog.DebugContext(ctx, "commit full audit compaction cutover", "run_id", journal.RunID)
	if err := advanceFullCompactJournal(
		&journal, auditstorage.CutoverCommitted, options.FailStep,
	); err != nil {
		durableJournal, exists, readErr := auditstorage.ReadCutoverJournal(journal.DatabasePath)
		if readErr != nil {
			return FullCompactResult{}, reportFullCompactError(
				"read full compaction journal after commit failure",
				errors.Join(err, readErr),
			)
		}
		if exists && auditstorage.IsCommittedCutoverPhase(durableJournal.Phase) {
			return FullCompactResult{}, reportFullCompactError("replacement committed; cleanup required", err)
		}
		return FullCompactResult{}, recoverPrecommitFullCompact(ctx, options, journal, err)
	}
	if err := cleanupCommittedFullCompact(&journal, options.FailStep); err != nil {
		return FullCompactResult{}, reportFullCompactError(
			"replacement committed; cleanup required",
			err,
		)
	}
	return FullCompactResult{
		RunID: journal.RunID, BeforeBytes: plan.DatabaseSize.DatabaseBytes,
		AfterBytes:     copyIdentity.Size,
		ReclaimedBytes: max(0, plan.DatabaseSize.DatabaseBytes-copyIdentity.Size),
	}, nil
}

func buildFullCompactCopy(
	ctx context.Context,
	database *sql.DB,
	workingPath string,
	failStep func(string) error,
) (int, error) {
	if err := checkpointFullCompactSource(ctx, database); err != nil {
		return 0, err
	}
	var sourceMode int
	if err := database.QueryRowContext(ctx, `pragma auto_vacuum`).Scan(&sourceMode); err != nil {
		return 0, wrapError("read source auto-vacuum mode", err)
	}
	if err := vacuumIntoFullCompact(ctx, database, workingPath); err != nil {
		return 0, err
	}
	working, err := openFullCompactDatabase(ctx, workingPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = working.Close() }()
	if _, err := working.ExecContext(ctx, `pragma auto_vacuum = incremental`); err != nil {
		return 0, wrapError("select incremental auto-vacuum for compact copy", err)
	}
	if err := failFullCompactHook(failStep, "before-vacuum"); err != nil {
		return 0, err
	}
	if _, err := working.ExecContext(ctx, `vacuum`); err != nil {
		return 0, wrapError("convert compact audit database to incremental auto-vacuum", err)
	}
	return sourceMode, nil
}

func validateFullCompactApplyOptions(options FullCompactApplyOptions) error {
	if strings.TrimSpace(options.Path) == "" || strings.TrimSpace(options.RuntimeDirectory) == "" {
		return errors.New("audit database path and runtime directory are required")
	}
	if strings.TrimSpace(options.Owner) == "" || options.LeaseTTL <= 0 {
		return errors.New("full compaction lease owner and duration are required")
	}
	if options.InspectService == nil {
		return errors.New("managed service inspection is required")
	}
	if options.Now == nil {
		return errors.New("full compaction clock is required")
	}
	return nil
}

func requireStoppedFullCompactService(service installer.ServiceState) error {
	if !service.Managed {
		return errors.New("full compaction requires a managed service")
	}
	if service.Running {
		return errors.New("managed daemon is running; stop it before full compaction")
	}
	return nil
}

func requireStoppedInspection(ctx context.Context, options FullCompactApplyOptions) error {
	service, err := options.InspectService(ctx)
	if err != nil {
		return reportFullCompactError("reinspect managed service before database replacement", err)
	}
	return requireStoppedFullCompactService(service)
}

func openFullCompactDatabase(ctx context.Context, path string) (*sql.DB, error) {
	uri := url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Set("_foreign_keys", "1")
	uri.RawQuery = query.Encode()
	database, err := sql.Open("sqlite3", uri.String())
	if err != nil {
		return nil, wrapError("open full compaction database", err)
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, wrapError("connect full compaction database", err)
	}
	return database, nil
}

func checkpointFullCompactSource(ctx context.Context, database *sql.DB) error {
	var busy int
	var logFrames int
	var checkpointed int
	if err := database.QueryRowContext(ctx, `pragma wal_checkpoint(truncate)`).Scan(
		&busy, &logFrames, &checkpointed,
	); err != nil {
		return wrapError("checkpoint full compaction source", err)
	}
	if busy != 0 {
		return errors.New("checkpoint full compaction source: database is busy")
	}
	return nil
}

func vacuumIntoFullCompact(ctx context.Context, database *sql.DB, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("full compaction working path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapError("inspect full compaction working path", err)
	}
	quoted := strings.ReplaceAll(path, "'", "''")
	// #nosec G202 -- SQLite does not allow binding the VACUUM INTO filename.
	statement := "vacuum into '" + quoted + "'"
	if _, err := database.ExecContext(ctx, statement); err != nil {
		return wrapError("build compact audit database", err)
	}
	return nil
}

func verifyFullCompactPair(ctx context.Context, sourcePath string, copyPath string, sourceMode int) error {
	source, err := openFullCompactDatabase(ctx, sourcePath)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()
	var actualSourceMode int
	if err := source.QueryRowContext(ctx, `pragma auto_vacuum`).Scan(&actualSourceMode); err != nil {
		return wrapError("read source auto-vacuum mode after compaction", err)
	}
	if actualSourceMode != sourceMode {
		return errors.New("source auto-vacuum mode changed during full compaction")
	}
	copyDatabase, err := openFullCompactDatabase(ctx, copyPath)
	if err != nil {
		return err
	}
	defer func() { _ = copyDatabase.Close() }()
	var copyMode int
	if err := copyDatabase.QueryRowContext(ctx, `pragma auto_vacuum`).Scan(&copyMode); err != nil {
		return wrapError("read compact copy auto-vacuum mode", err)
	}
	if copyMode != autoVacuumIncremental {
		return fmt.Errorf("compact copy auto-vacuum mode is %d, want %d", copyMode, autoVacuumIncremental)
	}
	return validateApplyDatabase(ctx, copyDatabase)
}

func finalizeFullCompactCopy(
	ctx context.Context,
	sourcePath string,
	workingPath string,
	sourceMode int,
) error {
	if err := preserveFullCompactMetadata(sourcePath, workingPath); err != nil {
		return err
	}
	if err := verifyFullCompactPair(ctx, sourcePath, workingPath, sourceMode); err != nil {
		return err
	}
	if err := syncFullCompactFile(workingPath); err != nil {
		return err
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(sourcePath)); err != nil {
		return wrapError("synchronize verified compact copy directory", err)
	}
	return nil
}

func preserveFullCompactMetadata(sourcePath string, copyPath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return wrapError("inspect source audit database metadata", err)
	}
	if err := os.Chmod(copyPath, info.Mode().Perm()); err != nil {
		return wrapError("preserve compact audit database permissions", err)
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		if err := os.Chown(copyPath, int(stat.Uid), int(stat.Gid)); err != nil {
			return wrapError("preserve compact audit database ownership", err)
		}
	}
	return nil
}

func syncFullCompactFile(path string) error {
	// #nosec G304 -- path is the unique working database sibling.
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return wrapError("open compact audit database for synchronization", err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil {
		return wrapError("synchronize compact audit database", err)
	}
	return nil
}

func fullCompactFileIdentity(path string) (auditstorage.FileIdentity, error) {
	identity, err := auditstorage.ReadFileIdentity(path)
	if err != nil {
		return auditstorage.FileIdentity{}, wrapError("read full compaction file identity", err)
	}
	return identity, nil
}

func readBoundFullCompactIdentity(
	path string,
	expected fullCompactIdentity,
) (auditstorage.FileIdentity, error) {
	if err := validateFullCompactIdentity(path, expected); err != nil {
		return auditstorage.FileIdentity{}, err
	}
	identity, err := fullCompactFileIdentity(path)
	if err != nil {
		return auditstorage.FileIdentity{}, err
	}
	if identity.Device != expected.device || identity.Inode != expected.inode {
		return auditstorage.FileIdentity{}, errors.New(
			"audit database path changed during full compaction",
		)
	}
	if err := validateFullCompactIdentity(path, expected); err != nil {
		return auditstorage.FileIdentity{}, err
	}
	return identity, nil
}

func releaseBoundFullCompactLease(
	ctx context.Context,
	path string,
	lease maintenanceLease,
	expected fullCompactIdentity,
) error {
	if !fullCompactIdentityStillMatches(path, expected) {
		return nil
	}
	return releaseFullCompactLease(ctx, path, lease)
}

func fullCompactIdentityStillMatches(path string, expected fullCompactIdentity) bool {
	actual, err := inspectFullCompactIdentity(path)
	return err == nil && actual == expected
}

func optionalFullCompactFileIdentity(path string) (*auditstorage.FileIdentity, error) {
	identity, err := fullCompactFileIdentity(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &identity, nil
}

func sameFullCompactIdentity(path string, expected auditstorage.FileIdentity) (bool, error) {
	actual, err := fullCompactFileIdentity(path)
	if err != nil {
		return false, err
	}
	return actual == expected, nil
}

func advanceFullCompactJournal(
	journal *auditstorage.CutoverJournal,
	phase auditstorage.CutoverPhase,
	failStep func(string) error,
) error {
	slog.Debug("advance full compaction journal", "phase", phase)
	if err := failFullCompactHook(failStep, "before-journal:"+string(phase)); err != nil {
		return err
	}
	updatedJournal := *journal
	updatedJournal.Phase = phase
	if err := auditstorage.WriteCutoverJournal(updatedJournal); err != nil {
		return wrapError("write full compaction journal phase", err)
	}
	*journal = updatedJournal
	return failFullCompactHook(failStep, "after-journal:"+string(phase))
}

func verifyInstalledFullCompact(
	ctx context.Context,
	journal auditstorage.CutoverJournal,
	requireContentIdentity bool,
) error {
	identity, err := fullCompactFileIdentity(journal.DatabasePath)
	if err != nil {
		return err
	}
	if requireContentIdentity && identity != journal.CopyIdentity {
		return errors.New("installed compact audit database identity does not match journal")
	}
	if !requireContentIdentity &&
		(identity.Device != journal.CopyIdentity.Device || identity.Inode != journal.CopyIdentity.Inode) {
		return errors.New("committed compact audit database filesystem identity does not match journal")
	}
	database, err := openFullCompactDatabase(ctx, journal.DatabasePath)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()
	return validateApplyDatabase(ctx, database)
}

func recoverPrecommitFullCompact(
	ctx context.Context,
	options FullCompactApplyOptions,
	journal auditstorage.CutoverJournal,
	cause error,
) error {
	_, recoveryErr := recoverFullCompact(ctx, options, journal)
	result := errors.Join(cause, recoveryErr)
	slog.WarnContext(ctx, "recover precommit full compaction", "err", result)
	return result
}

func recoverFullCompact(
	ctx context.Context,
	options FullCompactApplyOptions,
	journal auditstorage.CutoverJournal,
) (FullCompactResult, error) {
	slog.DebugContext(ctx, "recover full audit compaction", "phase", journal.Phase)
	if auditstorage.IsCommittedCutoverPhase(journal.Phase) {
		requireContentIdentity := !journal.ReplacementAccessAuthorized
		if err := verifyInstalledFullCompact(ctx, journal, requireContentIdentity); err != nil {
			return FullCompactResult{}, reportFullCompactError("committed replacement verification failed; preserve all recovery files", err)
		}
		if err := cleanupCommittedFullCompact(&journal, nil); err != nil {
			return FullCompactResult{}, err
		}
		return FullCompactResult{
			RunID: journal.RunID, BeforeBytes: 0, AfterBytes: 0, ReclaimedBytes: 0,
		}, nil
	}
	if err := restorePrecommitFullCompact(&journal, options.FailStep); err != nil {
		return FullCompactResult{}, reportFullCompactError("full compaction recovery failed; preserve all recovery files", err)
	}
	removeErr := removeFullCompactJournal(
		&journal,
		auditstorage.CutoverRemovingRestoredJournal,
		options.FailStep,
	)
	if removeErr != nil {
		if _, exists, readErr := auditstorage.ReadCutoverJournal(journal.DatabasePath); readErr != nil || exists {
			return FullCompactResult{}, wrapError("remove recovered full compaction journal", removeErr)
		}
	}
	lease := maintenanceLease{owner: options.Owner, runID: journal.RunID, ttl: options.LeaseTTL}
	if err := releaseFullCompactLease(ctx, journal.DatabasePath, lease); err != nil {
		return FullCompactResult{}, reportFullCompactError(
			"release recovered full compaction lease",
			errors.Join(removeErr, err),
		)
	}
	if removeErr != nil {
		return FullCompactResult{}, wrapError("remove recovered full compaction journal", removeErr)
	}
	return FullCompactResult{
		RunID: "", BeforeBytes: 0, AfterBytes: 0, ReclaimedBytes: 0,
	}, nil
}

func restorePrecommitFullCompact(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	databaseIsSource, err := preparePrecommitOriginalRestore(journal, failStep)
	if err != nil {
		slog.Warn("prepare precommit original restore failed", "err", err)
		return err
	}
	if !databaseIsSource {
		if err := restoreOriginalFullCompact(journal, failStep); err != nil {
			return err
		}
	}
	if err := restoreFullCompactSidecars(journal, failStep); err != nil {
		return err
	}
	if err := preserveFailedFullCompactCopy(journal, failStep); err != nil {
		return err
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize restored audit database", err)
	}
	return advanceFullCompactJournal(journal, auditstorage.CutoverRestored, failStep)
}

func preparePrecommitOriginalRestore(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) (bool, error) {
	_, err := os.Stat(journal.DatabasePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, wrapError("inspect canonical audit database during recovery", err)
	}
	matches, err := sameFullCompactIdentity(journal.DatabasePath, journal.SourceIdentity)
	if err != nil {
		return false, err
	}
	if matches {
		return true, nil
	}
	if err := preserveInstalledFullCompactReplacement(journal, failStep); err != nil {
		return false, err
	}
	return false, nil
}

func preserveInstalledFullCompactReplacement(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	slog.Debug("preserve installed full compaction replacement")
	copyMatches, err := sameFullCompactIdentity(journal.DatabasePath, journal.CopyIdentity)
	if err != nil || !copyMatches {
		return errors.New("unexpected database identity blocks full compaction recovery")
	}
	if _, err := os.Stat(journal.FailedPath); err == nil {
		return errors.New("failed replacement preservation path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapError("inspect failed replacement preservation path", err)
	}
	if err := advanceFullCompactJournal(
		journal,
		auditstorage.CutoverRestoringReplacement,
		failStep,
	); err != nil {
		return err
	}
	if err := failFullCompactHook(failStep, "before-rename:recovery-replacement"); err != nil {
		return err
	}
	if err := os.Rename(journal.DatabasePath, journal.FailedPath); err != nil {
		return wrapError("preserve failed compact replacement", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize preserved compact replacement", err)
	}
	if err := failFullCompactHook(failStep, "after-rename:recovery-replacement"); err != nil {
		return err
	}
	return advanceFullCompactJournal(
		journal,
		auditstorage.CutoverReplacementPreserved,
		failStep,
	)
}

func restoreOriginalFullCompact(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	slog.Debug("restore original full compaction database")
	matches, err := sameFullCompactIdentity(journal.RollbackPath, journal.SourceIdentity)
	if err != nil || !matches {
		return errors.New("verified rollback database is unavailable")
	}
	if err := advanceFullCompactJournal(
		journal,
		auditstorage.CutoverRestoringOriginal,
		failStep,
	); err != nil {
		return err
	}
	if err := failFullCompactHook(failStep, "before-rename:recovery-original"); err != nil {
		return err
	}
	if err := os.Rename(journal.RollbackPath, journal.DatabasePath); err != nil {
		return wrapError("restore original audit database", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize restored original audit database", err)
	}
	if err := failFullCompactHook(failStep, "after-rename:recovery-original"); err != nil {
		return err
	}
	return advanceFullCompactJournal(
		journal,
		auditstorage.CutoverOriginalRestored,
		failStep,
	)
}

func preserveFailedFullCompactCopy(
	journal *auditstorage.CutoverJournal,
	failStep func(string) error,
) error {
	slog.Debug("preserve failed full compaction copy")
	_, statErr := os.Stat(journal.WorkingPath)
	if errors.Is(statErr, os.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return wrapError("inspect failed compact working copy", statErr)
	}
	matches, err := sameFullCompactIdentity(journal.WorkingPath, journal.CopyIdentity)
	if err != nil {
		return err
	}
	if !matches {
		return errors.New("unexpected working database identity blocks full compaction recovery")
	}
	if _, err := os.Stat(journal.FailedPath); err == nil {
		return errors.New("failed replacement preservation path already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapError("inspect failed replacement preservation path", err)
	}
	if err := advanceFullCompactJournal(
		journal,
		auditstorage.CutoverPreservingWorking,
		failStep,
	); err != nil {
		return err
	}
	if err := failFullCompactHook(failStep, "before-rename:recovery-working"); err != nil {
		return err
	}
	if err := os.Rename(journal.WorkingPath, journal.FailedPath); err != nil {
		return wrapError("preserve failed compact copy", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(journal.DatabasePath)); err != nil {
		return wrapError("synchronize preserved failed compact copy", err)
	}
	if err := failFullCompactHook(failStep, "after-rename:recovery-working"); err != nil {
		return err
	}
	return advanceFullCompactJournal(journal, auditstorage.CutoverWorkingPreserved, failStep)
}

func preserveWorkingFullCompactFailure(
	workingPath string,
	failedPath string,
	databasePath string,
) error {
	slog.Debug("preserve full compaction working failure", "path", workingPath)
	if _, exists, err := auditstorage.ReadCutoverJournal(databasePath); err != nil {
		return wrapError("read full compaction journal before preserving failure", err)
	} else if exists {
		return nil
	}
	if _, err := os.Stat(workingPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return wrapError("inspect failed full compaction working database", err)
	}
	if _, err := os.Stat(failedPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return wrapError("inspect failed full compaction preservation path", err)
	}
	if err := os.Rename(workingPath, failedPath); err != nil {
		return wrapError("preserve failed full compaction working database", err)
	}
	if err := auditstorage.SyncDirectory(filepath.Dir(databasePath)); err != nil {
		return wrapError("synchronize preserved full compaction failure", err)
	}
	return nil
}

func failFullCompactStep(options FullCompactApplyOptions, step string) error {
	return failFullCompactHook(options.FailStep, step)
}

func failFullCompactHook(failStep func(string) error, step string) error {
	if failStep == nil {
		return nil
	}
	return failStep(step)
}

func reportFullCompactError(action string, err error) error {
	result := wrapError(action, err)
	slog.Warn(action, "err", result)
	return result
}
