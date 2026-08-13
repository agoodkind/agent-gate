package auditstorage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// CutoverPhase identifies one durable full-compaction cutover state.
type CutoverPhase string

const (
	cutoverJournalVersion  = 1
	maxCutoverJournalBytes = 64 << 10

	// CutoverPrepared means the verified copy and journal are durable.
	CutoverPrepared CutoverPhase = "prepared"
	// CutoverOriginalRenaming precedes the original database rename.
	CutoverOriginalRenaming CutoverPhase = "original-renaming"
	// CutoverOriginalRenamed means the original is at the rollback path.
	CutoverOriginalRenamed CutoverPhase = "original-renamed"
	// CutoverWALMoving precedes the write-ahead log sidecar move.
	CutoverWALMoving CutoverPhase = "wal-moving"
	// CutoverWALMoved means the write-ahead log sidecar move completed.
	CutoverWALMoved CutoverPhase = "wal-moved"
	// CutoverSHMMoving precedes the shared-memory sidecar move.
	CutoverSHMMoving CutoverPhase = "shm-moving"
	// CutoverSHMMoved means the shared-memory sidecar move completed.
	CutoverSHMMoved CutoverPhase = "shm-moved"
	// CutoverInstalling precedes the replacement database rename.
	CutoverInstalling CutoverPhase = "replacement-installing"
	// CutoverInstalled means the replacement occupies the canonical path.
	CutoverInstalled CutoverPhase = "replacement-installed"
	// CutoverRestoringReplacement precedes preserving an uncommitted replacement.
	CutoverRestoringReplacement CutoverPhase = "restoring-replacement"
	// CutoverReplacementPreserved means the uncommitted replacement is preserved.
	CutoverReplacementPreserved CutoverPhase = "replacement-preserved"
	// CutoverRestoringOriginal precedes restoring the original database.
	CutoverRestoringOriginal CutoverPhase = "restoring-original"
	// CutoverOriginalRestored means the original database is canonical again.
	CutoverOriginalRestored CutoverPhase = "original-restored"
	// CutoverRestoringWAL precedes restoring the original write-ahead log.
	CutoverRestoringWAL CutoverPhase = "restoring-wal"
	// CutoverWALRestored means write-ahead log restoration completed.
	CutoverWALRestored CutoverPhase = "wal-restored"
	// CutoverRestoringSHM precedes restoring the original shared-memory sidecar.
	CutoverRestoringSHM CutoverPhase = "restoring-shm"
	// CutoverSHMRestored means shared-memory sidecar restoration completed.
	CutoverSHMRestored CutoverPhase = "shm-restored"
	// CutoverPreservingWorking precedes preserving an unused compact copy.
	CutoverPreservingWorking CutoverPhase = "preserving-working"
	// CutoverWorkingPreserved means the unused compact copy is preserved.
	CutoverWorkingPreserved CutoverPhase = "working-preserved"
	// CutoverRestored means precommit restoration and synchronization completed.
	CutoverRestored CutoverPhase = "restored"
	// CutoverRemovingRestoredJournal precedes removing a precommit recovery journal.
	CutoverRemovingRestoredJournal CutoverPhase = "removing-restored-journal"
	// CutoverCommitted makes the verified replacement authoritative.
	CutoverCommitted CutoverPhase = "committed"
	// CutoverCleaningRollback precedes rollback database cleanup.
	CutoverCleaningRollback CutoverPhase = "cleanup-rollback"
	// CutoverCleaningWAL precedes rollback write-ahead log cleanup.
	CutoverCleaningWAL CutoverPhase = "cleanup-wal"
	// CutoverCleaningSHM precedes rollback shared-memory cleanup.
	CutoverCleaningSHM CutoverPhase = "cleanup-shm"
	// CutoverCleaningWorking precedes stale working copy cleanup.
	CutoverCleaningWorking CutoverPhase = "cleanup-working"
	// CutoverCleaningFailed precedes stale failed copy cleanup after commit.
	CutoverCleaningFailed CutoverPhase = "cleanup-failed"
	// CutoverRollbackCleaned means rollback database cleanup completed.
	CutoverRollbackCleaned CutoverPhase = "cleanup-rollback-complete"
	// CutoverWALCleaned means rollback write-ahead log cleanup completed.
	CutoverWALCleaned CutoverPhase = "cleanup-wal-complete"
	// CutoverSHMCleaned means rollback shared-memory cleanup completed.
	CutoverSHMCleaned CutoverPhase = "cleanup-shm-complete"
	// CutoverWorkingCleaned means stale working copy cleanup completed.
	CutoverWorkingCleaned CutoverPhase = "cleanup-working-complete"
	// CutoverFailedCleaned means stale failed copy cleanup completed.
	CutoverFailedCleaned CutoverPhase = "cleanup-failed-complete"
	// CutoverCleaningJournal precedes removing a committed cleanup journal.
	CutoverCleaningJournal CutoverPhase = "cleanup-journal"
)

// FileIdentity binds a journal entry to exact content and filesystem identity.
type FileIdentity struct {
	Device string `json:"device"`
	Inode  string `json:"inode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// CutoverJournal is the synchronized recovery record for one full compaction.
type CutoverJournal struct {
	Version                     int           `json:"version"`
	RunID                       string        `json:"run_id"`
	DatabasePath                string        `json:"database_path"`
	WorkingPath                 string        `json:"working_path"`
	RollbackPath                string        `json:"rollback_path"`
	FailedPath                  string        `json:"failed_path"`
	ServicePlatform             string        `json:"service_platform"`
	ServiceBinary               string        `json:"service_binary"`
	ServiceRunning              bool          `json:"service_running"`
	SourceIdentity              FileIdentity  `json:"source_identity"`
	CopyIdentity                FileIdentity  `json:"copy_identity"`
	WALIdentity                 *FileIdentity `json:"wal_identity,omitempty"`
	SHMIdentity                 *FileIdentity `json:"shm_identity,omitempty"`
	ReplacementAccessAuthorized bool          `json:"replacement_access_authorized,omitempty"`
	Phase                       CutoverPhase  `json:"phase"`
}

// GuardDatabase rejects migration through an existing handle during precommit recovery.
func GuardDatabase(ctx context.Context, database *sql.DB) error {
	slog.DebugContext(ctx, "guard audit database cutover")
	if database == nil {
		return errors.New("audit storage database is required")
	}
	rows, err := database.QueryContext(ctx, `pragma database_list`)
	if err != nil {
		return reportCutoverError("inspect audit database path", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var sequence int
		var name string
		var path string
		if err := rows.Scan(&sequence, &name, &path); err != nil {
			return reportCutoverError("read audit database path", err)
		}
		if name == "main" && path != "" {
			return guardDatabasePath(path, true)
		}
	}
	if err := rows.Err(); err != nil {
		return reportCutoverError("iterate audit database paths", err)
	}
	return nil
}

// ErrCutoverRecoveryRequired blocks ordinary database access before commit.
var ErrCutoverRecoveryRequired = errors.New("full compaction recovery is required")

// CutoverJournalPath returns the canonical sibling journal path.
func CutoverJournalPath(databasePath string) string {
	return databasePath + ".compact.journal"
}

// GuardDatabasePath rejects ordinary access while a precommit journal exists.
func GuardDatabasePath(databasePath string) error {
	return guardDatabasePath(databasePath, false)
}

func guardDatabasePath(databasePath string, authorizeCommittedAccess bool) error {
	slog.Debug("guard audit database path cutover", "path", databasePath)
	journal, exists, err := ReadCutoverJournal(databasePath)
	if err != nil {
		return reportCutoverError("guard audit database path cutover", err)
	}
	if !exists {
		return nil
	}
	if IsCommittedCutoverPhase(journal.Phase) {
		if journal.ReplacementAccessAuthorized {
			return guardCommittedCutoverFilesystemIdentity(databasePath, journal.CopyIdentity)
		}
		if err := guardCommittedCutoverIdentity(databasePath, journal.CopyIdentity); err != nil {
			return err
		}
		if !authorizeCommittedAccess {
			return nil
		}
		journal.ReplacementAccessAuthorized = true
		if err := WriteCutoverJournal(journal); err != nil {
			return reportCutoverError("authorize committed compact audit database access", err)
		}
		return nil
	}
	result := fmt.Errorf("%w: journal phase %s", ErrCutoverRecoveryRequired, journal.Phase)
	slog.Warn("audit database cutover is unresolved", "err", result)
	return result
}

// IsCommittedCutoverPhase reports whether automatic rollback is forbidden.
func IsCommittedCutoverPhase(phase CutoverPhase) bool {
	return phase == CutoverCommitted || strings.HasPrefix(string(phase), "cleanup-")
}

// ReadCutoverJournal reads and validates an owner-only journal.
func ReadCutoverJournal(databasePath string) (CutoverJournal, bool, error) {
	slog.Debug("read full compaction journal", "database_path", databasePath)
	path := CutoverJournalPath(databasePath)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyCutoverJournal(), false, nil
	}
	if err != nil {
		return emptyCutoverJournal(), false, reportCutoverError("inspect full compaction journal", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return emptyCutoverJournal(), false, errors.New("full compaction journal access is unsafe")
	}
	if info.Size() > maxCutoverJournalBytes {
		return emptyCutoverJournal(), false, errors.New("full compaction journal is too large")
	}
	// #nosec G304 -- this is the exact configured database sibling journal.
	body, err := os.ReadFile(path)
	if err != nil {
		return emptyCutoverJournal(), false, reportCutoverError("read full compaction journal", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var journal CutoverJournal
	if err := decoder.Decode(&journal); err != nil {
		return emptyCutoverJournal(), false, reportCutoverError("parse full compaction journal", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return emptyCutoverJournal(), false, errors.New("full compaction journal has trailing data")
	}
	if err := validateCutoverJournal(journal, databasePath); err != nil {
		return emptyCutoverJournal(), false, err
	}
	return journal, true, nil
}

func validateCutoverJournal(journal CutoverJournal, databasePath string) error {
	if journal.Version != cutoverJournalVersion || !validCutoverPhase(journal.Phase) {
		return errors.New("full compaction journal version or phase is invalid")
	}
	if !sameCutoverDatabasePath(journal.DatabasePath, databasePath) ||
		journal.RunID == "" || strings.ContainsAny(journal.RunID, `/\\`) {
		return errors.New("full compaction journal identity is invalid")
	}
	prefix := journal.DatabasePath + ".compact." + journal.RunID
	if !sameCutoverDatabasePath(journal.WorkingPath, prefix+".working") ||
		!sameCutoverDatabasePath(journal.RollbackPath, prefix+".rollback") ||
		!sameCutoverDatabasePath(journal.FailedPath, prefix+".failed") {
		return errors.New("full compaction journal recovery paths are invalid")
	}
	return nil
}

func validCutoverPhase(phase CutoverPhase) bool {
	switch phase {
	case CutoverPrepared, CutoverOriginalRenaming, CutoverOriginalRenamed,
		CutoverWALMoving, CutoverWALMoved, CutoverSHMMoving, CutoverSHMMoved,
		CutoverInstalling, CutoverInstalled, CutoverRestoringReplacement,
		CutoverReplacementPreserved, CutoverRestoringOriginal, CutoverOriginalRestored,
		CutoverRestoringWAL, CutoverWALRestored, CutoverRestoringSHM, CutoverSHMRestored,
		CutoverPreservingWorking, CutoverWorkingPreserved, CutoverRestored,
		CutoverRemovingRestoredJournal, CutoverCommitted, CutoverCleaningRollback,
		CutoverCleaningWAL, CutoverCleaningSHM, CutoverCleaningWorking,
		CutoverCleaningFailed, CutoverRollbackCleaned, CutoverWALCleaned,
		CutoverSHMCleaned, CutoverWorkingCleaned, CutoverFailedCleaned,
		CutoverCleaningJournal:
		return true
	default:
		return false
	}
}

func sameCutoverDatabasePath(first string, second string) bool {
	canonical := func(path string) string {
		absolute, err := filepath.Abs(path)
		if err != nil {
			return filepath.Clean(path)
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
		if err != nil {
			return filepath.Clean(absolute)
		}
		return filepath.Join(parent, filepath.Base(absolute))
	}
	return canonical(first) == canonical(second)
}

// WriteCutoverJournal atomically synchronizes a journal and its parent directory.
func WriteCutoverJournal(journal CutoverJournal) error {
	slog.Debug("write full compaction journal", "phase", journal.Phase)
	if strings.TrimSpace(journal.DatabasePath) == "" || strings.TrimSpace(journal.RunID) == "" ||
		journal.Phase == "" {
		return errors.New("full compaction journal fields are required")
	}
	journal.Version = cutoverJournalVersion
	if journal.WorkingPath == "" && journal.RollbackPath == "" && journal.FailedPath == "" {
		prefix := journal.DatabasePath + ".compact." + journal.RunID
		journal.WorkingPath = prefix + ".working"
		journal.RollbackPath = prefix + ".rollback"
		journal.FailedPath = prefix + ".failed"
	}
	if err := validateCutoverJournal(journal, journal.DatabasePath); err != nil {
		return err
	}
	body, err := json.Marshal(journal)
	if err != nil {
		return reportCutoverError("encode full compaction journal", err)
	}
	path := CutoverJournalPath(journal.DatabasePath)
	temporaryPath := path + ".tmp"
	// #nosec G304 -- this is the exact journal temporary sibling.
	file, err := os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		if removeErr := os.Remove(temporaryPath); removeErr != nil {
			return reportCutoverError("remove stale full compaction journal temporary", removeErr)
		}
		file, err = os.OpenFile(temporaryPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	}
	if err != nil {
		return reportCutoverError("create full compaction journal", err)
	}
	cleanup := true
	defer func() {
		_ = file.Close()
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := file.Write(append(body, '\n')); err != nil {
		return reportCutoverError("write full compaction journal", err)
	}
	if err := file.Sync(); err != nil {
		return reportCutoverError("synchronize full compaction journal", err)
	}
	if err := file.Close(); err != nil {
		return reportCutoverError("close full compaction journal", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return reportCutoverError("install full compaction journal", err)
	}
	cleanup = false
	return SyncDirectory(filepath.Dir(path))
}

// RemoveCutoverJournal durably removes the journal.
func RemoveCutoverJournal(databasePath string) error {
	slog.Debug("remove full compaction journal", "database_path", databasePath)
	if err := os.Remove(CutoverJournalPath(databasePath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return reportCutoverError("remove full compaction journal", err)
	}
	return SyncDirectory(filepath.Dir(databasePath))
}

// SyncDirectory synchronizes directory entry changes.
func SyncDirectory(path string) error {
	slog.Debug("synchronize full compaction directory", "path", path)
	directory, err := os.Open(path)
	if err != nil {
		return reportCutoverError("open directory for synchronization", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return reportCutoverError("synchronize directory", err)
	}
	return nil
}

func emptyCutoverJournal() CutoverJournal {
	return CutoverJournal{
		Version: 0, RunID: "", DatabasePath: "", WorkingPath: "",
		RollbackPath: "", FailedPath: "", ServicePlatform: "", ServiceBinary: "",
		ServiceRunning:              false,
		SourceIdentity:              FileIdentity{Device: "", Inode: "", Size: 0, SHA256: ""},
		CopyIdentity:                FileIdentity{Device: "", Inode: "", Size: 0, SHA256: ""},
		WALIdentity:                 nil,
		SHMIdentity:                 nil,
		ReplacementAccessAuthorized: false,
		Phase:                       "",
	}
}

func reportCutoverError(action string, err error) error {
	result := fmt.Errorf("%s: %w", action, err)
	slog.Warn(action, "err", result)
	return result
}
