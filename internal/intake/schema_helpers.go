package intake

import (
	"context"
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
