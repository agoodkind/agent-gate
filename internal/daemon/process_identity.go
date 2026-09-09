package daemon

import (
	"log/slog"
	"sync"

	"goodkind.io/agent-gate/internal/version"
)

var buildIdentityWarning sync.Once

func initializeBuildIdentity(log *slog.Logger) {
	if err := version.Initialize(); err != nil {
		buildIdentityWarning.Do(func() {
			log.Warn("build identity unavailable", slog.Any("err", err))
		})
	}
}
