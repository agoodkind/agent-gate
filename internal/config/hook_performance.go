package config

import (
	"errors"
	"fmt"
	"runtime"
	"time"

	"goodkind.io/agent-gate/internal/hotkv"
)

const (
	defaultHookMinimumHotConcurrency = 4
	defaultHookHotConcurrencyFactor  = 4
	defaultHookHotQueueWait          = 25 * time.Millisecond
	defaultHookInferencePhaseTimeout = 4 * time.Second
	defaultHookDeferredQueueLimit    = 8192
	defaultHookDeferredWorkers       = 1
	maxHookInferencePhaseTimeoutMS   = 9000
)

// HookHotConcurrency returns the daemon admission limit for synchronous hook
// evaluation.
func (c *Config) HookHotConcurrency() int {
	if c != nil && c.Performance.Hook.HotConcurrency > 0 {
		return c.Performance.Hook.HotConcurrency
	}
	limit := runtime.GOMAXPROCS(0) * defaultHookHotConcurrencyFactor
	if limit < defaultHookMinimumHotConcurrency {
		return defaultHookMinimumHotConcurrency
	}
	return limit
}

// HookHotQueueWait returns the maximum time a hook waits for a hot-path slot.
func (c *Config) HookHotQueueWait() time.Duration {
	if c != nil && c.Performance.Hook.HotQueueWaitMS > 0 {
		return time.Duration(c.Performance.Hook.HotQueueWaitMS) * time.Millisecond
	}
	return defaultHookHotQueueWait
}

// HookInferencePhaseTimeout returns the shared deadline for infer-bearing hot rules.
func (c *Config) HookInferencePhaseTimeout() time.Duration {
	if c != nil && c.Performance.Hook.InferencePhaseTimeoutMS > 0 {
		milliseconds := min(
			c.Performance.Hook.InferencePhaseTimeoutMS,
			maxHookInferencePhaseTimeoutMS,
		)
		return time.Duration(milliseconds) * time.Millisecond
	}
	return defaultHookInferencePhaseTimeout
}

// HookDeferredQueueLimit returns the bounded queue size for cool audit work.
func (c *Config) HookDeferredQueueLimit() int {
	if c != nil && c.Performance.Hook.DeferredQueueLimit > 0 {
		return c.Performance.Hook.DeferredQueueLimit
	}
	return defaultHookDeferredQueueLimit
}

// HookDeferredWorkers returns the number of workers that process cool audit work.
func (c *Config) HookDeferredWorkers() int {
	if c != nil && c.Performance.Hook.DeferredWorkers > 0 {
		return c.Performance.Hook.DeferredWorkers
	}
	return defaultHookDeferredWorkers
}

// HookCacheMaxEntries returns the maximum daemon hot cache entry count.
func (c *Config) HookCacheMaxEntries() int {
	if c != nil && c.Performance.Hook.Cache.MaxEntries > 0 {
		return c.Performance.Hook.Cache.MaxEntries
	}
	return hotkv.DefaultMaxEntries
}

// HookCacheMaxValueBytes returns the maximum bytes accepted per hot cache value.
func (c *Config) HookCacheMaxValueBytes() int {
	if c != nil && c.Performance.Hook.Cache.MaxValueBytes > 0 {
		return c.Performance.Hook.Cache.MaxValueBytes
	}
	return hotkv.DefaultMaxValueBytes
}

// HookCachePruneInterval returns the daemon hot cache periodic prune interval.
func (c *Config) HookCachePruneInterval() time.Duration {
	if c != nil && c.Performance.Hook.Cache.PruneIntervalMS > 0 {
		return time.Duration(c.Performance.Hook.Cache.PruneIntervalMS) * time.Millisecond
	}
	return hotkv.DefaultPruneInterval
}

func validateHookPerformance(performance HookPerformance) error {
	if performance.InferencePhaseTimeoutMS < 0 {
		return errors.New("performance.hook.inference_phase_timeout_ms must be non-negative")
	}
	if performance.InferencePhaseTimeoutMS > maxHookInferencePhaseTimeoutMS {
		return fmt.Errorf(
			"performance.hook.inference_phase_timeout_ms must not exceed %d",
			maxHookInferencePhaseTimeoutMS,
		)
	}
	return nil
}
