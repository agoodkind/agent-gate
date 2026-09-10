package daemon

import "log/slog"

// The baseline predates eager process identity; BuildHash still reads each call.
func initializeBuildIdentity(_ *slog.Logger) {}
