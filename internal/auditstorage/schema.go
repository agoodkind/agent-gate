package auditstorage

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schemaSQL string

// Initialize creates the current schema in one transaction.
func Initialize(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return errors.New("audit storage database is required")
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin audit schema", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, schemaSQL); err != nil {
		return storageError("create audit schema", err)
	}
	if err := transaction.Commit(); err != nil {
		return storageError("commit audit schema", err)
	}
	return nil
}

// OpenWriter opens one serialized writer and initializes only a new file.
func OpenWriter(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, storageError("create audit directory", err)
	}
	_, err := os.Stat(path)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return nil, storageError("stat audit database", err)
	}
	location := url.URL{Scheme: "file", Path: path}
	values := url.Values{}
	values.Set("_foreign_keys", "on")
	values.Set("_journal_mode", "WAL")
	values.Set("_synchronous", "NORMAL")
	values.Set("_busy_timeout", "5000")
	location.RawQuery = values.Encode()
	database, err := sql.Open("sqlite3", location.String())
	if err != nil {
		return nil, storageError("open audit database", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, storageError("connect audit database", err)
	}
	if fresh {
		if err := Initialize(ctx, database); err != nil {
			_ = database.Close()
			return nil, err
		}
	}
	return database, nil
}

func storageError(message string, err error) error {
	slog.Warn(message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
