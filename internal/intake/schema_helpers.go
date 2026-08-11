package intake

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

func ensureIntakeEventColumn(
	ctx context.Context,
	db *sql.DB,
	columnName string,
	definition string,
) error {
	rows, err := db.QueryContext(ctx, `pragma table_info(intake_events)`)
	if err != nil {
		return wrapError("query intake_events schema", err)
	}
	defer func() {
		_ = rows.Close()
	}()

	for rows.Next() {
		var (
			columnID   int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			primaryKey int
		)
		if err := rows.Scan(
			&columnID,
			&name,
			&columnType,
			&notNull,
			&defaultVal,
			&primaryKey,
		); err != nil {
			return wrapError("scan intake_events schema", err)
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return wrapError("iterate intake_events schema", err)
	}

	statement := fmt.Sprintf(
		"alter table intake_events add column %s %s",
		columnName,
		definition,
	)
	if _, err := db.ExecContext(ctx, statement); err != nil {
		return wrapError("add intake_events."+columnName, err)
	}
	return nil
}

func wrapLoggedError(
	ctx context.Context,
	log *slog.Logger,
	message string,
	err error,
) error {
	if err == nil {
		return nil
	}
	if log != nil {
		log.WarnContext(ctx, message+" failed", "err", err)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	slog.Warn(message+" failed", "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
