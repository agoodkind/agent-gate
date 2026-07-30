package rules

import (
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// backgroundValidatorTimeout bounds a validator run that outlived its event's
// synchronous budget. The run continues detached so its verdict can land in
// the cache and decide the next event for the same target. The value comes
// from performance.timeouts.exec_background_ms; the constant is only the
// fallback for a runtime whose config failed to load.
func backgroundValidatorTimeout() time.Duration {
	cfg, err := config.Load()
	if err != nil {
		return config.DefaultExecBackgroundMs * time.Millisecond
	}
	return cfg.ExecBackgroundTimeout()
}
