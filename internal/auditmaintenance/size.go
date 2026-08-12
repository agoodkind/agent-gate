package auditmaintenance

import (
	"context"
	"database/sql"
	"errors"
	"os"
)

// DatabaseSize reports physical and compacted audit storage usage.
type DatabaseSize struct {
	DatabaseBytes       int64
	WALBytes            int64
	PageSizeBytes       int64
	PageCount           int64
	FreePages           int64
	CompactedUsageBytes int64
}

// MeasureDatabaseSize checkpoints available WAL frames and measures live storage.
func MeasureDatabaseSize(path string) (DatabaseSize, error) {
	ctx := context.Background()
	database, err := openApplyDatabase(ctx, path)
	if err != nil {
		return DatabaseSize{}, err
	}
	defer func() { _ = database.Close() }()
	size, err := measureDatabaseSize(ctx, database, 0, 0)
	if err != nil {
		return DatabaseSize{}, err
	}
	size.DatabaseBytes, err = fileSize(path)
	if err != nil {
		return DatabaseSize{}, err
	}
	size.WALBytes, err = optionalFileSize(path + "-wal")
	if err != nil {
		return DatabaseSize{}, err
	}
	return size, nil
}

func measureDatabaseSize(
	ctx context.Context,
	database *sql.DB,
	databaseBytes int64,
	walBytes int64,
) (DatabaseSize, error) {
	var busy int64
	var walFrames int64
	var checkpointedFrames int64
	if err := database.QueryRowContext(ctx, `pragma wal_checkpoint(passive)`).Scan(
		&busy,
		&walFrames,
		&checkpointedFrames,
	); err != nil {
		return DatabaseSize{}, classifyMaintenanceWriteError(
			"checkpoint audit write-ahead log for size measurement",
			err,
		)
	}
	if busy != 0 || walFrames < 0 || checkpointedFrames < 0 {
		return DatabaseSize{}, ErrMaintenanceBusy
	}
	var size DatabaseSize
	size.DatabaseBytes = databaseBytes
	size.WALBytes = walBytes
	if err := database.QueryRowContext(ctx, `pragma page_size`).Scan(&size.PageSizeBytes); err != nil {
		return DatabaseSize{}, wrapError("read audit page size", err)
	}
	if err := database.QueryRowContext(ctx, `pragma page_count`).Scan(&size.PageCount); err != nil {
		return DatabaseSize{}, wrapError("read audit page count", err)
	}
	if err := database.QueryRowContext(ctx, `pragma freelist_count`).Scan(&size.FreePages); err != nil {
		return DatabaseSize{}, wrapError("read audit free pages", err)
	}
	liveWALFrames := max(walFrames-checkpointedFrames, 0)
	size.CompactedUsageBytes = (size.PageCount-size.FreePages)*size.PageSizeBytes + liveWALFrames*size.PageSizeBytes
	return size, nil
}

func measureApplyDatabaseSize(
	ctx context.Context,
	database *sql.DB,
	path string,
) (DatabaseSize, error) {
	size, err := measureDatabaseSize(ctx, database, 0, 0)
	if err != nil {
		return DatabaseSize{}, err
	}
	size.DatabaseBytes, err = fileSize(path)
	if err != nil {
		return DatabaseSize{}, err
	}
	size.WALBytes, err = optionalFileSize(path + "-wal")
	if err != nil {
		return DatabaseSize{}, err
	}
	return size, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, wrapError("stat audit database", err)
	}
	return info.Size(), nil
}

func optionalFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, wrapError("stat audit write-ahead log", err)
	}
	return info.Size(), nil
}
