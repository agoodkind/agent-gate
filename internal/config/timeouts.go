package config

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// HookEvaluateTimeoutFromFile returns the hook client's per-call deadline,
// reading only the performance table from the config file.
//
// The hook process is transport-only: it forwards an event to the daemon and
// prints the answer. Calling Load here would decode the whole file and compile
// every rule pattern through PCRE2 on the hook's critical path, for one
// integer, which is work the daemon already owns. This decodes into a struct
// carrying just the timeouts instead.
//
// The result is resolved once per process. A hook process handles one event, so
// that is one read rather than one per RPC. Any read or decode failure yields
// the documented default rather than an unbounded call.
var HookEvaluateTimeoutFromFile = sync.OnceValue(func() time.Duration {
	milliseconds := DefaultHookEvaluateMs
	sourceBytes, err := os.ReadFile(Path())
	if err == nil {
		var probe struct {
			Performance struct {
				Timeouts TimeoutPerformance `toml:"timeouts"`
			} `toml:"performance"`
		}
		if _, decodeErr := toml.Decode(string(sourceBytes), &probe); decodeErr == nil {
			milliseconds = positiveOr(probe.Performance.Timeouts.HookEvaluateMS, DefaultHookEvaluateMs)
		}
	}
	return time.Duration(milliseconds) * time.Millisecond
})

// Enforcement-path timeout defaults. Each is the value applied when config
// leaves the matching key unset, and each is reachable through
// [performance.timeouts] so a deployment can move it without a new binary.
const (
	DefaultHookEvaluateMs    = 12000
	DefaultExecTimeoutMs     = 1500
	DefaultMaxExecTimeoutMs  = 4000
	DefaultExecBackgroundMs  = 30000
	DefaultMaxExecRetryCount = 5
	DefaultInferTimeoutMs    = 1500
	DefaultMaxInferTimeoutMs = 8000
	DefaultExecCacheKey      = "effective_cwd"
)

// TimeoutPerformance carries every deadline and bound the enforcement path
// applies, so none of them is fixed at build time. A value left at zero takes
// the matching Default constant.
//
// The bounds exist because a validator that outlives the hook deadline makes
// the hook give up, and a rule that fails open loses its verdict. Keeping them
// here rather than in Go constants means a config can never require a binary
// that has not shipped: raising ExecMaxMS raises the ceiling it is checked
// against, in the same file, at the same time.
type TimeoutPerformance struct {
	HookEvaluateMS    int `toml:"hook_evaluate_ms"`
	ExecDefaultMS     int `toml:"exec_default_ms"`
	ExecMaxMS         int `toml:"exec_max_ms"`
	ExecBackgroundMS  int `toml:"exec_background_ms"`
	ExecMaxRetryCount int `toml:"exec_max_retry_count"`
	InferDefaultMS    int `toml:"infer_default_ms"`
	InferMaxMS        int `toml:"infer_max_ms"`
}

func positiveOr(value int, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func (c *Config) timeouts() TimeoutPerformance {
	if c == nil {
		return TimeoutPerformance{
			HookEvaluateMS: 0, ExecDefaultMS: 0, ExecMaxMS: 0, ExecBackgroundMS: 0,
			ExecMaxRetryCount: 0, InferDefaultMS: 0, InferMaxMS: 0,
		}
	}
	return c.Performance.Timeouts
}

// HookEvaluateTimeout returns the deadline the hook client applies to one
// daemon evaluation. Every per-condition timeout must fit inside it.
func (c *Config) HookEvaluateTimeout() time.Duration {
	milliseconds := positiveOr(c.timeouts().HookEvaluateMS, DefaultHookEvaluateMs)
	return time.Duration(milliseconds) * time.Millisecond
}

// ExecDefaultTimeoutMs returns the timeout an exec condition takes when it sets
// none.
func (c *Config) ExecDefaultTimeoutMs() int {
	return positiveOr(c.timeouts().ExecDefaultMS, DefaultExecTimeoutMs)
}

// ExecMaxTimeoutMs returns the largest timeout an exec condition may request.
func (c *Config) ExecMaxTimeoutMs() int {
	return positiveOr(c.timeouts().ExecMaxMS, DefaultMaxExecTimeoutMs)
}

// ExecBackgroundTimeout returns the deadline for one expanded validator run
// after evaluation has moved off the synchronous path.
func (c *Config) ExecBackgroundTimeout() time.Duration {
	milliseconds := positiveOr(c.timeouts().ExecBackgroundMS, DefaultExecBackgroundMs)
	return time.Duration(milliseconds) * time.Millisecond
}

// ExecMaxRetryCount returns the retry ceiling for an exec condition, which
// bounds how many validators one misconfigured rule can fork.
func (c *Config) ExecMaxRetryCount() int {
	return positiveOr(c.timeouts().ExecMaxRetryCount, DefaultMaxExecRetryCount)
}

// InferDefaultTimeoutMs returns the timeout an infer condition takes when it
// sets none.
func (c *Config) InferDefaultTimeoutMs() int {
	return positiveOr(c.timeouts().InferDefaultMS, DefaultInferTimeoutMs)
}

// InferMaxTimeoutMs returns the largest timeout an infer condition may request.
func (c *Config) InferMaxTimeoutMs() int {
	return positiveOr(c.timeouts().InferMaxMS, DefaultMaxInferTimeoutMs)
}

// validateTimeouts rejects a timeout set that cannot hold. Every value must be
// non-negative, and both per-condition ceilings must stay strictly under the
// hook deadline, because a condition allowed to outlive that deadline fails
// open and discards the verdict it was configured to produce.
func validateTimeouts(timeouts TimeoutPerformance) error {
	fields := []struct {
		name  string
		value int
	}{
		{"hook_evaluate_ms", timeouts.HookEvaluateMS},
		{"exec_default_ms", timeouts.ExecDefaultMS},
		{"exec_max_ms", timeouts.ExecMaxMS},
		{"exec_background_ms", timeouts.ExecBackgroundMS},
		{"exec_max_retry_count", timeouts.ExecMaxRetryCount},
		{"infer_default_ms", timeouts.InferDefaultMS},
		{"infer_max_ms", timeouts.InferMaxMS},
	}
	for _, field := range fields {
		if field.value < 0 {
			return fmt.Errorf("performance.timeouts.%s must be non-negative", field.name)
		}
	}

	hookEvaluate := positiveOr(timeouts.HookEvaluateMS, DefaultHookEvaluateMs)
	execMax := positiveOr(timeouts.ExecMaxMS, DefaultMaxExecTimeoutMs)
	inferMax := positiveOr(timeouts.InferMaxMS, DefaultMaxInferTimeoutMs)
	execDefault := positiveOr(timeouts.ExecDefaultMS, DefaultExecTimeoutMs)
	inferDefault := positiveOr(timeouts.InferDefaultMS, DefaultInferTimeoutMs)

	if execMax >= hookEvaluate {
		return fmt.Errorf(
			"performance.timeouts.exec_max_ms %d must stay under hook_evaluate_ms %d",
			execMax, hookEvaluate,
		)
	}
	if inferMax >= hookEvaluate {
		return fmt.Errorf(
			"performance.timeouts.infer_max_ms %d must stay under hook_evaluate_ms %d",
			inferMax, hookEvaluate,
		)
	}
	if execDefault > execMax {
		return fmt.Errorf(
			"performance.timeouts.exec_default_ms %d exceeds exec_max_ms %d",
			execDefault, execMax,
		)
	}
	if inferDefault > inferMax {
		return fmt.Errorf(
			"performance.timeouts.infer_default_ms %d exceeds infer_max_ms %d",
			inferDefault, inferMax,
		)
	}
	return nil
}
