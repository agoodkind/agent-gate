package auditstorage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
)

// ErrFileBusy means a reader or writer still owns the file.
var ErrFileBusy = errors.New("audit file still has a reader or writer")

// ErrFileComplete prevents writers from reopening completed history.
var ErrFileComplete = errors.New("audit file is complete")

// Completed reads durable journal mode without opening SQLite.
func Completed(path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, os.ErrNotExist
		}
		return false, storageError("read audit completion header", err)
	}
	defer func() { _ = file.Close() }()
	var header [100]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return false, storageError("read audit completion header", err)
	}
	if string(header[:16]) != "SQLite format 3\x00" {
		return false, fmt.Errorf("invalid SQLite header in %s", path)
	}
	return header[18] == 1 && header[19] == 1, nil
}

// Finalize checkpoints retired files only after every pending receipt finishes.
// Shared borrowers must close before this operation requests exclusive access.
func (catalog *Catalog) Finalize(ctx context.Context, bucket Bucket) (bool, error) {
	coordination, err := catalog.coordinate(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = coordination.Close() }()
	state, err := catalog.loadStateSummary(ctx)
	if err != nil {
		return false, err
	}
	if state.ResetPending {
		return false, ErrResetPending
	}
	matched, valid := parseBucket(catalog.options.BasePath, filepath.Base(bucket.Path))
	if state.BasePath != catalog.options.BasePath || !valid || matched != bucket {
		return false, ErrPolicyChanged
	}
	if state.CurrentBucketID == bucket.ID {
		return false, nil
	}
	lock := catalog.bucketLock(bucket.Path)
	defer func() { _ = lock.Close() }()
	locked, err := lock.TryLock()
	if err != nil {
		return false, storageError("lock audit finalization", err)
	}
	if !locked {
		return false, ErrFileBusy
	}
	if complete, err := Completed(bucket.Path); complete || err != nil {
		return complete, err
	}
	location := url.URL{Scheme: "file", Path: bucket.Path}
	location.RawQuery = url.Values{"mode": {"rw"}, "_busy_timeout": {"0"}, "_synchronous": {"NORMAL"}}.Encode()
	database, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return false, storageError("open audit finalization", err)
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	var pending bool
	if err := database.QueryRowContext(ctx, completionPendingSQL).Scan(&pending); err != nil {
		return false, storageError("check durable pending receipts", err)
	}
	if pending {
		return false, nil
	}
	var busy, frames, checkpointed int
	if err := database.QueryRowContext(ctx, completionCheckpointSQL).Scan(&busy, &frames, &checkpointed); err != nil {
		return false, storageError("checkpoint completed audit file", err)
	}
	if busy != 0 {
		return false, ErrFileBusy
	}
	if err := catalog.checkpoint("finalize", bucket.Path); err != nil {
		return false, err
	}
	var mode string
	if err := database.QueryRowContext(ctx, completionJournalSQL).Scan(&mode); err != nil {
		return false, storageError("convert completed audit journal", err)
	}
	if mode != "delete" {
		return false, fmt.Errorf("completed audit journal mode is %s", mode)
	}
	if err := database.Close(); err != nil {
		return false, storageError("close completed audit file", err)
	}
	return Completed(bucket.Path)
}

//go:embed completion_pending.sql
var completionPendingSQL string

//go:embed completion_checkpoint.sql
var completionCheckpointSQL string

//go:embed completion_journal.sql
var completionJournalSQL string
