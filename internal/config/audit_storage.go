package config

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

const (
	// AuditStorageProfileFull retains all detail classes.
	AuditStorageProfileFull AuditStorageProfile = "full"
	// AuditStorageProfileMinimal removes detail after terminal completion.
	AuditStorageProfileMinimal   AuditStorageProfile = "minimal"
	defaultAuditBucketInterval                       = 24 * time.Hour
	defaultAuditRetentionBuckets                     = 7
)

// AuditStorageProfile selects the baseline detail policy.
type AuditStorageProfile string

// AuditStorage holds raw values from the [audit.storage] TOML table.
type AuditStorage struct {
	Profile          string             `toml:"profile"`
	BucketInterval   *string            `toml:"bucket_interval"`
	RetentionBuckets *int               `toml:"retention_buckets"`
	Detail           AuditStorageDetail `toml:"detail"`
}

// AuditStorageDetail holds optional detail-class overrides.
type AuditStorageDetail struct {
	WireInput           *bool `toml:"wire_input"`
	NormalizedInput     *bool `toml:"normalized_input"`
	ProviderEvidence    *bool `toml:"provider_evidence"`
	EnvironmentEvidence *bool `toml:"environment_evidence"`
	EvaluationContent   *bool `toml:"evaluation_content"`
}

// AuditStorageDetailPolicy is the resolved retention policy for detail classes.
type AuditStorageDetailPolicy struct {
	WireInput           bool
	NormalizedInput     bool
	ProviderEvidence    bool
	EnvironmentEvidence bool
	EvaluationContent   bool
}

// AuditStoragePolicy is the validated effective audit storage policy.
type AuditStoragePolicy struct {
	Profile          AuditStorageProfile
	BucketInterval   time.Duration
	RetentionBuckets int
	Detail           AuditStorageDetailPolicy
}

func resolveAuditStorage(raw AuditStorage) (AuditStoragePolicy, error) {
	profile := AuditStorageProfile(raw.Profile)
	if profile == "" {
		profile = AuditStorageProfileFull
	}
	var policy AuditStoragePolicy
	policy.Profile = profile
	policy.BucketInterval = defaultAuditBucketInterval
	policy.RetentionBuckets = defaultAuditRetentionBuckets
	switch profile {
	case AuditStorageProfileFull:
		policy.Detail = AuditStorageDetailPolicy{
			WireInput:           true,
			NormalizedInput:     true,
			ProviderEvidence:    true,
			EnvironmentEvidence: true,
			EvaluationContent:   true,
		}
	case AuditStorageProfileMinimal:
	default:
		return AuditStoragePolicy{}, fmt.Errorf("audit.storage.profile: expected %q or %q, got %q", AuditStorageProfileFull, AuditStorageProfileMinimal, profile)
	}
	if raw.BucketInterval != nil {
		interval, err := time.ParseDuration(*raw.BucketInterval)
		if err != nil {
			slog.Warn("invalid audit bucket interval", "err", err)
			return AuditStoragePolicy{}, fmt.Errorf("audit.storage.bucket_interval: %w", err)
		}
		policy.BucketInterval = interval
	}
	if raw.RetentionBuckets != nil {
		policy.RetentionBuckets = *raw.RetentionBuckets
	}
	if err := policy.Rotation().Validate(); err != nil {
		slog.Warn("invalid audit rotation policy", "err", err)
		return AuditStoragePolicy{}, fmt.Errorf("audit.storage: %w", err)
	}
	applyAuditStorageDetailOverrides(&policy.Detail, raw.Detail)
	return policy, nil
}

func applyAuditStorageDetailOverrides(policy *AuditStorageDetailPolicy, raw AuditStorageDetail) {
	if raw.WireInput != nil {
		policy.WireInput = *raw.WireInput
	}
	if raw.NormalizedInput != nil {
		policy.NormalizedInput = *raw.NormalizedInput
	}
	if raw.ProviderEvidence != nil {
		policy.ProviderEvidence = *raw.ProviderEvidence
	}
	if raw.EnvironmentEvidence != nil {
		policy.EnvironmentEvidence = *raw.EnvironmentEvidence
	}
	if raw.EvaluationContent != nil {
		policy.EvaluationContent = *raw.EvaluationContent
	}
}

// Rotation adapts the validated configuration without coupling storage to config.
func (policy AuditStoragePolicy) Rotation() auditstorage.RotationPolicy {
	return auditstorage.RotationPolicy{Interval: policy.BucketInterval, Retained: policy.RetentionBuckets}
}

// AuditCatalogOptions uses stable per-user state and coordination paths.
func (c *Config) AuditCatalogOptions() auditstorage.CatalogOptions {
	return auditstorage.CatalogOptions{
		BasePath:         c.AuditSQLitePath(),
		StatePath:        filepath.Join(DefaultStateDir(), "audit-storage.json"),
		CoordinationPath: filepath.Join(filepath.Dir(RuntimeDir()), "agent-gate-audit.lock"),
	}
}

// AuditStoragePolicy returns the policy resolved during configuration load.
func (c *Config) AuditStoragePolicy() AuditStoragePolicy {
	if c == nil {
		var policy AuditStoragePolicy
		return policy
	}
	return c.auditStoragePolicy
}
