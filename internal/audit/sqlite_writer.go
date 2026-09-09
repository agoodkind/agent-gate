package audit

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

//go:embed insert_event.sql
var insertEventSQL string

//go:embed insert_operation.sql
var insertOperationSQL string

//go:embed insert_decision.sql
var insertDecisionSQL string

//go:embed insert_violation.sql
var insertViolationSQL string

type sqliteWriter struct {
	db     *sql.DB
	ownsDB bool
}

func (writer *sqliteWriter) Close() error {
	if writer.ownsDB {
		if err := writer.db.Close(); err != nil {
			return storageError("close audit writer", err)
		}
	}
	return nil
}

// WriteEvents stores a batch in one transaction.
func WriteEvents(ctx context.Context, database *sql.DB, events []Event) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return storageError("begin audit batch", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if err := WriteEventsInTx(ctx, transaction, events); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return storageError("commit audit batch", err)
	}
	return nil
}

// WriteEventsInTx inserts events and children without committing the caller's transaction.
func WriteEventsInTx(ctx context.Context, transaction *sql.Tx, events []Event) error {
	for _, event := range events {
		if event.Time != "" {
			instant, err := time.Parse(time.RFC3339Nano, event.Time)
			if err != nil {
				return storageError("parse audit event time", err)
			}
			event.Time = auditstorage.FormatTime(instant)
		}
		result, err := transaction.ExecContext(ctx, insertEventSQL,
			event.EventID, event.SchemaVersion, event.Time, event.Level, event.Message,
			event.System, event.SessionID, event.TurnID, event.EventName, event.ToolUseID,
			event.ToolName, event.RawPayloadHash)
		if err != nil {
			return storageError("insert audit event", err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return storageError("read audit insertion result", err)
		}
		if count == 0 {
			continue
		}
		checked, err := json.Marshal(event.Decision.RulesChecked)
		if err != nil {
			return storageError("encode checked rules", err)
		}
		matched, err := json.Marshal(event.Decision.RulesMatched)
		if err != nil {
			return storageError("encode matched rules", err)
		}
		if _, err := transaction.ExecContext(ctx, insertOperationSQL, event.EventID,
			event.Operation.CWD, event.Operation.EffectiveCWD, event.Operation.Command,
			event.Operation.FilePath); err != nil {
			return storageError("insert audit operation", err)
		}
		if _, err := transaction.ExecContext(ctx, insertDecisionSQL, event.EventID,
			event.Decision.Kind, event.Decision.CanBlock, string(checked),
			string(matched)); err != nil {
			return storageError("insert audit decision", err)
		}
		for _, violation := range event.Violations {
			if _, err := transaction.ExecContext(ctx, insertViolationSQL, event.EventID,
				violation.Rule, violation.Mode, violation.FieldPath, violation.FilePath,
				violation.Start, violation.End, violation.Message); err != nil {
				return storageError("insert audit violation", err)
			}
		}
	}
	return nil
}

func storageError(message string, err error) error {
	slog.Warn(message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
