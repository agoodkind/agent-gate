package config

import (
	"fmt"
	"time"
)

// Size and count bounds applied across the daemon. Each is the value used when
// config leaves the matching key unset, and each is reachable through
// [performance.limits].
//
// Every key here is read by the code that enforces it. A knob whose value
// nothing consumes is worse than no knob, because setting it looks like it
// worked. Bounds that are still compiled in, including the shell-read byte cap,
// the upstream and evaluation-layer metadata sizes, the evaluation query
// limits, and the install and service readiness polling, are deliberately
// absent until their call sites read them.
const (
	DefaultRegexMatchLimit         = 100000
	DefaultRegexDepthLimit         = 4096
	DefaultAuditQueueLimit         = 8192
	DefaultAuditDedupCacheSize     = 4096
	DefaultHookInferencePhaseMaxMs = 9000
)

// Intervals and leases the daemon applies to background work. Each is
// reachable through [performance.intervals] and read by its consumer.
const (
	DefaultAuditDedupTTLMs        = 30000
	DefaultAuditDropLogIntervalMs = 5000
	DefaultOverloadLogIntervalMs  = 5000
	DefaultDeferredClaimLeaseMs   = 30000
	DefaultDeferredClaimRenewMs   = 10000
)

// LimitPerformance carries the size and count bounds the daemon enforces. A
// value left at zero takes the matching Default constant.
type LimitPerformance struct {
	RegexMatchLimit         int `toml:"regex_match_limit"`
	RegexDepthLimit         int `toml:"regex_depth_limit"`
	AuditQueueLimit         int `toml:"audit_queue_limit"`
	AuditDedupCacheSize     int `toml:"audit_dedup_cache_size"`
	HookInferencePhaseMaxMs int `toml:"hook_inference_phase_max_ms"`
}

// IntervalPerformance carries the periods and leases background work uses.
type IntervalPerformance struct {
	AuditDedupTTLMs        int `toml:"audit_dedup_ttl_ms"`
	AuditDropLogIntervalMs int `toml:"audit_drop_log_interval_ms"`
	OverloadLogIntervalMs  int `toml:"overload_log_interval_ms"`
	DeferredClaimLeaseMs   int `toml:"deferred_claim_lease_ms"`
	DeferredClaimRenewMs   int `toml:"deferred_claim_renew_ms"`
}

func (c *Config) limits() LimitPerformance {
	if c == nil {
		return LimitPerformance{
			RegexMatchLimit: 0, RegexDepthLimit: 0, AuditQueueLimit: 0,
			AuditDedupCacheSize: 0, HookInferencePhaseMaxMs: 0,
		}
	}
	return c.Performance.Limits
}

func (c *Config) intervals() IntervalPerformance {
	if c == nil {
		return IntervalPerformance{
			AuditDedupTTLMs: 0, AuditDropLogIntervalMs: 0, OverloadLogIntervalMs: 0,
			DeferredClaimLeaseMs: 0, DeferredClaimRenewMs: 0,
		}
	}
	return c.Performance.Intervals
}

// RegexMatchLimit returns the backtracking match budget for one regex run.
func (c *Config) RegexMatchLimit() int {
	return positiveOr(c.limits().RegexMatchLimit, DefaultRegexMatchLimit)
}

// RegexDepthLimit returns the recursion depth budget for one regex run.
func (c *Config) RegexDepthLimit() int {
	return positiveOr(c.limits().RegexDepthLimit, DefaultRegexDepthLimit)
}

// AuditQueueLimit returns the bounded audit write queue size.
func (c *Config) AuditQueueLimit() int {
	return positiveOr(c.limits().AuditQueueLimit, DefaultAuditQueueLimit)
}

// AuditDedupCacheSize returns the audit duplicate-suppression cache size.
func (c *Config) AuditDedupCacheSize() int {
	return positiveOr(c.limits().AuditDedupCacheSize, DefaultAuditDedupCacheSize)
}

// HookInferencePhaseMaxMs returns the ceiling on the shared inference phase
// deadline.
func (c *Config) HookInferencePhaseMaxMs() int {
	return positiveOr(c.limits().HookInferencePhaseMaxMs, DefaultHookInferencePhaseMaxMs)
}

func millisecondsOr(value int, fallback int) time.Duration {
	return time.Duration(positiveOr(value, fallback)) * time.Millisecond
}

// AuditDedupTTL returns how long an audit duplicate stays suppressed.
func (c *Config) AuditDedupTTL() time.Duration {
	return millisecondsOr(c.intervals().AuditDedupTTLMs, DefaultAuditDedupTTLMs)
}

// AuditDropLogInterval returns how often dropped audit writes are logged.
func (c *Config) AuditDropLogInterval() time.Duration {
	return millisecondsOr(c.intervals().AuditDropLogIntervalMs, DefaultAuditDropLogIntervalMs)
}

// OverloadLogInterval returns how often daemon overload is logged.
func (c *Config) OverloadLogInterval() time.Duration {
	return millisecondsOr(c.intervals().OverloadLogIntervalMs, DefaultOverloadLogIntervalMs)
}

// DeferredClaimLease returns how long a deferred-intake claim is held.
func (c *Config) DeferredClaimLease() time.Duration {
	return millisecondsOr(c.intervals().DeferredClaimLeaseMs, DefaultDeferredClaimLeaseMs)
}

// DeferredClaimRenewInterval returns how often a deferred claim is renewed.
func (c *Config) DeferredClaimRenewInterval() time.Duration {
	return millisecondsOr(c.intervals().DeferredClaimRenewMs, DefaultDeferredClaimRenewMs)
}

// validateLimits rejects a negative bound and the one relationship that can
// silently break background work: a deferred claim that renews no sooner than
// its own lease expires, which lets the claim lapse mid-run.
func validateLimits(limits LimitPerformance, intervals IntervalPerformance) error {
	numbered := []struct {
		name  string
		value int
	}{
		{"limits.regex_match_limit", limits.RegexMatchLimit},
		{"limits.regex_depth_limit", limits.RegexDepthLimit},
		{"limits.audit_queue_limit", limits.AuditQueueLimit},
		{"limits.audit_dedup_cache_size", limits.AuditDedupCacheSize},
		{"limits.hook_inference_phase_max_ms", limits.HookInferencePhaseMaxMs},
		{"intervals.audit_dedup_ttl_ms", intervals.AuditDedupTTLMs},
		{"intervals.audit_drop_log_interval_ms", intervals.AuditDropLogIntervalMs},
		{"intervals.overload_log_interval_ms", intervals.OverloadLogIntervalMs},
		{"intervals.deferred_claim_lease_ms", intervals.DeferredClaimLeaseMs},
		{"intervals.deferred_claim_renew_ms", intervals.DeferredClaimRenewMs},
	}
	for _, field := range numbered {
		if field.value < 0 {
			return fmt.Errorf("performance.%s must be non-negative", field.name)
		}
	}

	claimLease := positiveOr(intervals.DeferredClaimLeaseMs, DefaultDeferredClaimLeaseMs)
	claimRenew := positiveOr(intervals.DeferredClaimRenewMs, DefaultDeferredClaimRenewMs)
	if claimRenew >= claimLease {
		return fmt.Errorf(
			"performance.intervals.deferred_claim_renew_ms %d must stay under deferred_claim_lease_ms %d",
			claimRenew, claimLease,
		)
	}
	return nil
}
