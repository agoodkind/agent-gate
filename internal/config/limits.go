package config

import (
	"fmt"
	"time"
)

// Size and count bounds applied across the daemon. Each is the value used when
// config leaves the matching key unset, and each is reachable through
// [performance.limits].
const (
	DefaultRegexMatchLimit           = 100000
	DefaultRegexDepthLimit           = 4096
	DefaultShellReadMaxBytes         = 1048576
	DefaultUpstreamMetadataJSONBytes = 4096
	DefaultLayerMetadataJSONBytes    = 16384
	DefaultLayerMetadataStringBytes  = 1024
	DefaultAuditQueueLimit           = 8192
	DefaultAuditDedupCacheSize       = 4096
	DefaultEvaluationQueryLimit      = 50
	DefaultEvaluationMaxQueryLimit   = 1000
	DefaultHookInferencePhaseMaxMs   = 9000
)

// Intervals and leases the daemon applies to background work. Each is
// reachable through [performance.intervals].
const (
	DefaultAuditDedupTTLMs            = 30000
	DefaultAuditDropLogIntervalMs     = 5000
	DefaultOverloadLogIntervalMs      = 5000
	DefaultDeferredClaimLeaseMs       = 30000
	DefaultDeferredClaimRenewMs       = 10000
	DefaultServiceWaitAttempts        = 50
	DefaultServiceWaitSleepMs         = 200
	DefaultInstallReadinessTimeoutMs  = 10000
	DefaultInstallReadinessIntervalMs = 200
)

// LimitPerformance carries the size and count bounds the daemon enforces. A
// value left at zero takes the matching Default constant.
type LimitPerformance struct {
	RegexMatchLimit           int `toml:"regex_match_limit"`
	RegexDepthLimit           int `toml:"regex_depth_limit"`
	ShellReadMaxBytes         int `toml:"shell_read_max_bytes"`
	UpstreamMetadataJSONBytes int `toml:"upstream_metadata_json_bytes"`
	LayerMetadataJSONBytes    int `toml:"layer_metadata_json_bytes"`
	LayerMetadataStringBytes  int `toml:"layer_metadata_string_bytes"`
	AuditQueueLimit           int `toml:"audit_queue_limit"`
	AuditDedupCacheSize       int `toml:"audit_dedup_cache_size"`
	EvaluationQueryLimit      int `toml:"evaluation_query_limit"`
	EvaluationMaxQueryLimit   int `toml:"evaluation_max_query_limit"`
	HookInferencePhaseMaxMs   int `toml:"hook_inference_phase_max_ms"`
}

// IntervalPerformance carries the periods and leases background work uses.
type IntervalPerformance struct {
	AuditDedupTTLMs            int `toml:"audit_dedup_ttl_ms"`
	AuditDropLogIntervalMs     int `toml:"audit_drop_log_interval_ms"`
	OverloadLogIntervalMs      int `toml:"overload_log_interval_ms"`
	DeferredClaimLeaseMs       int `toml:"deferred_claim_lease_ms"`
	DeferredClaimRenewMs       int `toml:"deferred_claim_renew_ms"`
	ServiceWaitAttempts        int `toml:"service_wait_attempts"`
	ServiceWaitSleepMs         int `toml:"service_wait_sleep_ms"`
	InstallReadinessTimeoutMs  int `toml:"install_readiness_timeout_ms"`
	InstallReadinessIntervalMs int `toml:"install_readiness_interval_ms"`
}

func (c *Config) limits() LimitPerformance {
	if c == nil {
		return LimitPerformance{
			RegexMatchLimit: 0, RegexDepthLimit: 0, ShellReadMaxBytes: 0,
			UpstreamMetadataJSONBytes: 0, LayerMetadataJSONBytes: 0,
			LayerMetadataStringBytes: 0, AuditQueueLimit: 0, AuditDedupCacheSize: 0,
			EvaluationQueryLimit: 0, EvaluationMaxQueryLimit: 0,
			HookInferencePhaseMaxMs: 0,
		}
	}
	return c.Performance.Limits
}

func (c *Config) intervals() IntervalPerformance {
	if c == nil {
		return IntervalPerformance{
			AuditDedupTTLMs: 0, AuditDropLogIntervalMs: 0, OverloadLogIntervalMs: 0,
			DeferredClaimLeaseMs: 0, DeferredClaimRenewMs: 0, ServiceWaitAttempts: 0,
			ServiceWaitSleepMs: 0, InstallReadinessTimeoutMs: 0,
			InstallReadinessIntervalMs: 0,
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

// ShellReadMaxBytes returns the most bytes a shell-read concern will inspect.
func (c *Config) ShellReadMaxBytes() int {
	return positiveOr(c.limits().ShellReadMaxBytes, DefaultShellReadMaxBytes)
}

// UpstreamMetadataJSONBytes returns the accepted size of upstream metadata JSON.
func (c *Config) UpstreamMetadataJSONBytes() int {
	return positiveOr(c.limits().UpstreamMetadataJSONBytes, DefaultUpstreamMetadataJSONBytes)
}

// LayerMetadataJSONBytes returns the accepted size of one evaluation layer's
// metadata JSON.
func (c *Config) LayerMetadataJSONBytes() int {
	return positiveOr(c.limits().LayerMetadataJSONBytes, DefaultLayerMetadataJSONBytes)
}

// LayerMetadataStringBytes returns the accepted size of one metadata string.
func (c *Config) LayerMetadataStringBytes() int {
	return positiveOr(c.limits().LayerMetadataStringBytes, DefaultLayerMetadataStringBytes)
}

// AuditQueueLimit returns the bounded audit write queue size.
func (c *Config) AuditQueueLimit() int {
	return positiveOr(c.limits().AuditQueueLimit, DefaultAuditQueueLimit)
}

// AuditDedupCacheSize returns the audit duplicate-suppression cache size.
func (c *Config) AuditDedupCacheSize() int {
	return positiveOr(c.limits().AuditDedupCacheSize, DefaultAuditDedupCacheSize)
}

// EvaluationQueryLimit returns the default row count for an evaluation query.
func (c *Config) EvaluationQueryLimit() int {
	return positiveOr(c.limits().EvaluationQueryLimit, DefaultEvaluationQueryLimit)
}

// EvaluationMaxQueryLimit returns the largest row count an evaluation query may
// request.
func (c *Config) EvaluationMaxQueryLimit() int {
	return positiveOr(c.limits().EvaluationMaxQueryLimit, DefaultEvaluationMaxQueryLimit)
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

// ServiceWaitAttempts returns how many times install polls for the service.
func (c *Config) ServiceWaitAttempts() int {
	return positiveOr(c.intervals().ServiceWaitAttempts, DefaultServiceWaitAttempts)
}

// ServiceWaitSleep returns the pause between service readiness polls.
func (c *Config) ServiceWaitSleep() time.Duration {
	return millisecondsOr(c.intervals().ServiceWaitSleepMs, DefaultServiceWaitSleepMs)
}

// InstallReadinessTimeout returns how long install waits for the daemon.
func (c *Config) InstallReadinessTimeout() time.Duration {
	return millisecondsOr(c.intervals().InstallReadinessTimeoutMs, DefaultInstallReadinessTimeoutMs)
}

// InstallReadinessInterval returns the pause between install readiness polls.
func (c *Config) InstallReadinessInterval() time.Duration {
	return millisecondsOr(c.intervals().InstallReadinessIntervalMs, DefaultInstallReadinessIntervalMs)
}

// validateLimits rejects a negative bound and the one relationship that can
// silently truncate results: a default query limit above its own ceiling.
func validateLimits(limits LimitPerformance, intervals IntervalPerformance) error {
	numbered := []struct {
		name  string
		value int
	}{
		{"limits.regex_match_limit", limits.RegexMatchLimit},
		{"limits.regex_depth_limit", limits.RegexDepthLimit},
		{"limits.shell_read_max_bytes", limits.ShellReadMaxBytes},
		{"limits.upstream_metadata_json_bytes", limits.UpstreamMetadataJSONBytes},
		{"limits.layer_metadata_json_bytes", limits.LayerMetadataJSONBytes},
		{"limits.layer_metadata_string_bytes", limits.LayerMetadataStringBytes},
		{"limits.audit_queue_limit", limits.AuditQueueLimit},
		{"limits.audit_dedup_cache_size", limits.AuditDedupCacheSize},
		{"limits.evaluation_query_limit", limits.EvaluationQueryLimit},
		{"limits.evaluation_max_query_limit", limits.EvaluationMaxQueryLimit},
		{"limits.hook_inference_phase_max_ms", limits.HookInferencePhaseMaxMs},
		{"intervals.audit_dedup_ttl_ms", intervals.AuditDedupTTLMs},
		{"intervals.audit_drop_log_interval_ms", intervals.AuditDropLogIntervalMs},
		{"intervals.overload_log_interval_ms", intervals.OverloadLogIntervalMs},
		{"intervals.deferred_claim_lease_ms", intervals.DeferredClaimLeaseMs},
		{"intervals.deferred_claim_renew_ms", intervals.DeferredClaimRenewMs},
		{"intervals.service_wait_attempts", intervals.ServiceWaitAttempts},
		{"intervals.service_wait_sleep_ms", intervals.ServiceWaitSleepMs},
		{"intervals.install_readiness_timeout_ms", intervals.InstallReadinessTimeoutMs},
		{"intervals.install_readiness_interval_ms", intervals.InstallReadinessIntervalMs},
	}
	for _, field := range numbered {
		if field.value < 0 {
			return fmt.Errorf("performance.%s must be non-negative", field.name)
		}
	}

	queryDefault := positiveOr(limits.EvaluationQueryLimit, DefaultEvaluationQueryLimit)
	queryMax := positiveOr(limits.EvaluationMaxQueryLimit, DefaultEvaluationMaxQueryLimit)
	if queryDefault > queryMax {
		return fmt.Errorf(
			"performance.limits.evaluation_query_limit %d exceeds evaluation_max_query_limit %d",
			queryDefault, queryMax,
		)
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
