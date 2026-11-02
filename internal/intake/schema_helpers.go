package intake

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
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
	if log != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		log.WarnContext(ctx, message+" failed", "err", err)
	}
	return fmt.Errorf("%s: %w", message, err)
}

func wrapError(message string, err error) error {
	if err == nil {
		return nil
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		slog.Warn(message+" failed", "err", err)
	}
	return fmt.Errorf("%s: %w", message, err)
}
