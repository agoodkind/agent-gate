package intake

import (
	"context"
	"database/sql"
	"log/slog"
)

func logDeferredReceiptRepairs(ctx context.Context, database *sql.DB, log *slog.Logger) {
	if log == nil {
		return
	}
	var repairCount int
	err := database.QueryRowContext(ctx, `
		select count(*) from intake_deferred_repairs
		where state = ? and repair_error = 'missing_receipt'
	`, DeferredStatePending).Scan(&repairCount)
	if err != nil || repairCount == 0 {
		return
	}
	log.WarnContext(
		ctx,
		"legacy deferred rows require receipt repair",
		"repair_error", "missing_receipt",
		"repair_count", repairCount,
	)
}
