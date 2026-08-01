package rules

import (
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// SetBackgroundTimeout stores the deadline for one validator run that outlived
// its event's synchronous budget. The daemon calls it when a runtime snapshot
// is built, so a config reload updates it in place, and it is safe to call on a
// live runtime because it takes the runtime mutex.
//
// The value lives on the runtime rather than being read from config at the call
// site because the call site is a retry loop. Reading it there would reload,
// decode, and recompile the whole config once per attempt.
func (r *ExecRuntime) SetBackgroundTimeout(timeout time.Duration) {
	if r == nil || timeout <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backgroundTimeoutValue = timeout
}

// backgroundTimeout returns the deadline for one expanded validator run after
// evaluation has moved off the synchronous path. The run continues detached so
// its verdict can land in the cache and decide the next event for the same
// target.
//
// A runtime built outside the daemon never has the setter called, so an unset
// value falls back to the documented default rather than to zero, which would
// give every background validator an immediate deadline.
func (r *ExecRuntime) backgroundTimeout() time.Duration {
	if r == nil {
		return config.DefaultExecBackgroundMs * time.Millisecond
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.backgroundTimeoutValue <= 0 {
		return config.DefaultExecBackgroundMs * time.Millisecond
	}
	return r.backgroundTimeoutValue
}
