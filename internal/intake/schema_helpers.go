package intake

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func wrapLoggedError(
	ctx context.Context,
	log *slog.Logger,
	message string,
	err error,
) error {
	if err == nil {
		return nil
	}

	if log != nil && !auditstorage.MatchesOnly(err, ctx.Err()) {
		log.WarnContext(ctx, message+" failed", "err", err)
	}

	return fmt.Errorf("%s: %w", message, err)
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}

	if !auditstorage.CancellationOnly(err) {
		slog.Warn(message+" failed", "err", err)
	}

	return fmt.Errorf("%s: %w", message, err)
}
