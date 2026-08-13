package auditstorage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

const migrationTimeFormat = time.RFC3339Nano

const migrationCleanupTimeout = 5 * time.Second

var migrations = []Migration{
	{
		Version: 1, ForeignKeysDisabled: false, Apply: migrateV1,
		AfterCommit: reportLegacyQuarantine,
	},
	{Version: 2, ForeignKeysDisabled: true, Apply: migrateIntakeV2, AfterCommit: nil},
	{Version: 3, ForeignKeysDisabled: true, Apply: migrateEvaluationV3, AfterCommit: nil},
	{Version: 4, ForeignKeysDisabled: true, Apply: migrateOutboxV4, AfterCommit: nil},
	{Version: 5, ForeignKeysDisabled: false, Apply: migrateMaintenanceV5, AfterCommit: nil},
	{Version: 6, ForeignKeysDisabled: false, Apply: migrateScheduleV6, AfterCommit: nil},
}

var migrationNow = time.Now

// Migrate applies every pending audit schema version in order.
func Migrate(ctx context.Context, database *sql.DB) error {
	return migrate(ctx, database, 5000)
}

// MigrateNonblocking applies pending versions without waiting for another writer.
func MigrateNonblocking(ctx context.Context, database *sql.DB) error {
	return migrate(ctx, database, 0)
}

func migrate(ctx context.Context, database *sql.DB, busyTimeoutMilliseconds int) error {
	if database == nil {
		return errors.New("audit storage database is required")
	}
	if err := GuardDatabase(ctx, database); err != nil {
		return err
	}
	if err := configureDatabase(ctx, database, busyTimeoutMilliseconds); err != nil {
		return err
	}
	version, err := SchemaVersion(ctx, database)
	if err != nil {
		return err
	}
	if version > 0 {
		if _, err := MigrationAppliedAt(ctx, database, version); err != nil {
			return err
		}
	}
	for _, migration := range migrations {
		if migration.Version <= version {
			continue
		}
		if err := applyMigration(ctx, database, migration); err != nil {
			return err
		}
		version = migration.Version
	}
	return nil
}

// SchemaVersion returns the newest recorded audit schema version.
func SchemaVersion(ctx context.Context, database *sql.DB) (int, error) {
	if database == nil {
		return 0, errors.New("audit storage database is required")
	}
	exists, err := schemaObjectExists(ctx, database, "audit_schema_migrations")
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, nil
	}
	var version int
	if err := database.QueryRowContext(
		ctx,
		`select coalesce(max(version), 0) from audit_schema_migrations`,
	).Scan(&version); err != nil {
		return 0, wrapError("read audit schema version", err)
	}
	return version, nil
}

// MigrationAppliedAt returns when one audit schema version committed.
func MigrationAppliedAt(
	ctx context.Context,
	database *sql.DB,
	version int,
) (time.Time, error) {
	if database == nil {
		return time.Time{}, errors.New("audit storage database is required")
	}
	var raw string
	if err := database.QueryRowContext(
		ctx,
		`select applied_at from audit_schema_migrations where version = ?`,
		version,
	).Scan(&raw); err != nil {
		return time.Time{}, wrapError(
			fmt.Sprintf("read audit migration %d application time", version),
			err,
		)
	}
	appliedAt, err := time.Parse(migrationTimeFormat, raw)
	if err != nil {
		return time.Time{}, wrapError(
			fmt.Sprintf("parse audit migration %d application time", version),
			err,
		)
	}
	return appliedAt, nil
}

func configureDatabase(
	ctx context.Context,
	database *sql.DB,
	busyTimeoutMilliseconds int,
) error {
	newDatabase, err := hasNoApplicationTables(ctx, database)
	if err != nil {
		return err
	}
	if newDatabase {
		if _, err := database.ExecContext(ctx, `pragma auto_vacuum = incremental`); err != nil {
			return wrapError("enable incremental auto-vacuum", err)
		}
	}
	statements := []string{
		fmt.Sprintf("pragma busy_timeout = %d", busyTimeoutMilliseconds),
		`pragma journal_mode = wal`,
		`pragma foreign_keys = on`,
	}
	for _, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return wrapError("configure audit storage", err)
		}
	}
	var foreignKeysEnabled int
	if err := database.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&foreignKeysEnabled); err != nil {
		return wrapError("verify audit storage foreign keys", err)
	}
	if foreignKeysEnabled != 1 {
		return errors.New("audit storage foreign keys are disabled")
	}
	return nil
}

func hasNoApplicationTables(ctx context.Context, database *sql.DB) (bool, error) {
	var count int
	if err := database.QueryRowContext(ctx, `
		select count(*)
		from sqlite_schema
		where type = 'table'
			and name not like 'sqlite_%'
			and name != 'audit_schema_migrations'
	`).Scan(&count); err != nil {
		return false, wrapError("inspect audit storage tables", err)
	}
	return count == 0, nil
}

func schemaObjectExists(ctx context.Context, database *sql.DB, name string) (bool, error) {
	var count int
	if err := database.QueryRowContext(
		ctx,
		`select count(*) from sqlite_schema where name = ?`,
		name,
	).Scan(&count); err != nil {
		return false, wrapError(fmt.Sprintf("inspect audit schema object %q", name), err)
	}
	return count != 0, nil
}

func applyMigration(
	ctx context.Context,
	database *sql.DB,
	migration Migration,
) (returnErr error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return wrapError(fmt.Sprintf("reserve audit migration %d connection", migration.Version), err)
	}
	defer func() {
		if err := connection.Close(); err != nil && returnErr == nil {
			returnErr = wrapError(
				fmt.Sprintf("close audit migration %d connection", migration.Version),
				err,
			)
		}
	}()
	return applyMigrationOnConnection(ctx, connection, migration)
}

func applyMigrationOnConnection(
	ctx context.Context,
	connection *sql.Conn,
	migration Migration,
) (returnErr error) {
	if migration.ForeignKeysDisabled {
		if _, err := connection.ExecContext(ctx, `pragma foreign_keys = off`); err != nil {
			return wrapError(fmt.Sprintf("disable audit migration %d foreign keys", migration.Version), err)
		}
		defer func() {
			cleanupContext, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				migrationCleanupTimeout,
			)
			defer cancel()
			if err := restoreMigrationForeignKeys(
				cleanupContext,
				connection,
				migration.Version,
			); err != nil {
				returnErr = errors.Join(returnErr, err)
			}
		}()
	}
	if err := ctx.Err(); err != nil {
		return wrapError(fmt.Sprintf("begin audit migration %d", migration.Version), err)
	}
	transaction, err := connection.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return wrapError(fmt.Sprintf("begin audit migration %d", migration.Version), err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, `
		create table if not exists audit_schema_migrations (
			version integer primary key,
			applied_at text not null
		)
	`); err != nil {
		return wrapError("create audit schema migration ledger", err)
	}
	if err := migration.Apply(ctx, transaction); err != nil {
		return wrapError(fmt.Sprintf("apply audit migration %d", migration.Version), err)
	}
	if err := verifyForeignKeys(ctx, transaction); err != nil {
		return wrapError(fmt.Sprintf("verify audit migration %d foreign keys", migration.Version), err)
	}
	if _, err := transaction.ExecContext(
		ctx,
		`insert into audit_schema_migrations (version, applied_at) values (?, ?)`,
		migration.Version,
		migrationNow().UTC().Format(migrationTimeFormat),
	); err != nil {
		return wrapError(fmt.Sprintf("record audit migration %d", migration.Version), err)
	}
	userVersionStatement := fmt.Sprintf("pragma user_version = %d", migration.Version)
	if _, err := transaction.ExecContext(ctx, userVersionStatement); err != nil {
		return wrapError(fmt.Sprintf("record audit user version %d", migration.Version), err)
	}
	if err := ctx.Err(); err != nil {
		return wrapError(fmt.Sprintf("commit audit migration %d", migration.Version), err)
	}
	if err := transaction.Commit(); err != nil {
		return wrapError(fmt.Sprintf("commit audit migration %d", migration.Version), err)
	}
	if migration.AfterCommit != nil {
		migration.AfterCommit(context.WithoutCancel(ctx), connection)
	}
	return nil
}

func restoreMigrationForeignKeys(
	ctx context.Context,
	connection *sql.Conn,
	version int,
) error {
	if _, err := connection.ExecContext(ctx, `pragma foreign_keys = on`); err != nil {
		return wrapError(fmt.Sprintf("restore audit migration %d foreign keys", version), err)
	}
	var enabled int
	if err := connection.QueryRowContext(ctx, `pragma foreign_keys`).Scan(&enabled); err != nil {
		return wrapError(fmt.Sprintf("verify audit migration %d foreign keys", version), err)
	}
	if enabled != 1 {
		return fmt.Errorf("audit migration %d foreign keys remain disabled", version)
	}
	return nil
}

func verifyForeignKeys(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx, `pragma foreign_key_check`)
	if err != nil {
		return wrapError("query audit foreign keys", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		var table string
		var rowID sql.NullInt64
		var parent string
		var constraintID int
		if err := rows.Scan(&table, &rowID, &parent, &constraintID); err != nil {
			return wrapError("scan audit foreign key violation", err)
		}
		return fmt.Errorf(
			"foreign key violation in %s row %d referencing %s constraint %d",
			table,
			rowID.Int64,
			parent,
			constraintID,
		)
	}
	if err := rows.Err(); err != nil {
		return wrapError("iterate audit foreign keys", err)
	}
	return nil
}

func wrapError(message string, err error) error {
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
