package config

import (
	"fmt"
	"log/slog"
	"math"
	"time"
)

const (
	// AuditStorageProfileBalanced selects seven-day full-detail retention by default.
	AuditStorageProfileBalanced AuditStorageProfile = "balanced"
	// AuditStorageProfileFull selects full-detail retention for the summary window.
	AuditStorageProfileFull AuditStorageProfile = "full"
	// AuditStorageProfileMinimal selects detail removal after terminal completion.
	AuditStorageProfileMinimal AuditStorageProfile = "minimal"

	defaultAuditMaintenanceInterval        = 24 * time.Hour
	defaultAuditMaintenanceBatchRows       = 1000
	defaultAuditFullRetention              = 720 * time.Hour
	defaultAuditBalancedRetention          = 168 * time.Hour
	defaultAuditSummaryRetention           = 720 * time.Hour
	auditStorageBytesPerMB           int64 = 1_000_000
)

// AuditStorageProfile selects the baseline retention and detail policy.
type AuditStorageProfile string

// AuditStorage holds raw values from the [audit.storage] TOML table.
type AuditStorage struct {
	Profile                 string             `toml:"profile"`
	MaintenanceInterval     *string            `toml:"maintenance_interval"`
	MaxSizeMB               *int64             `toml:"max_size_mb"`
	MaintenanceBatchRows    *int               `toml:"maintenance_batch_rows"`
	CompactAfterMaintenance *bool              `toml:"compact_after_maintenance"`
	FullDetailRetention     *string            `toml:"full_detail_retention"`
	SummaryRetention        *string            `toml:"summary_retention"`
	Detail                  AuditStorageDetail `toml:"detail"`
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
	Profile                 AuditStorageProfile
	MaintenanceInterval     time.Duration
	MaxSizeBytes            int64
	MaintenanceBatchRows    int
	CompactAfterMaintenance bool
	FullDetailRetention     time.Duration
	SummaryRetention        time.Duration
	Detail                  AuditStorageDetailPolicy
}

func resolveAuditStorage(raw AuditStorage) (AuditStoragePolicy, error) {
	profile := AuditStorageProfile(raw.Profile)
	if profile == "" {
		profile = AuditStorageProfileBalanced
	}

	policy, err := auditStorageProfilePolicy(profile)
	if err != nil {
		return AuditStoragePolicy{}, err
	}

	if raw.MaintenanceInterval != nil {
		policy.MaintenanceInterval, err = parseAuditStorageDuration(
			"maintenance_interval",
			*raw.MaintenanceInterval,
		)
		if err != nil {
			return AuditStoragePolicy{}, err
		}
	}
	if raw.MaxSizeMB != nil {
		if *raw.MaxSizeMB < 0 {
			return AuditStoragePolicy{}, fmt.Errorf("audit.storage.max_size_mb must not be negative")
		}
		if *raw.MaxSizeMB > math.MaxInt64/auditStorageBytesPerMB {
			return AuditStoragePolicy{}, fmt.Errorf("audit.storage.max_size_mb is too large")
		}
		policy.MaxSizeBytes = *raw.MaxSizeMB * auditStorageBytesPerMB
	}
	if raw.MaintenanceBatchRows != nil {
		if *raw.MaintenanceBatchRows <= 0 {
			return AuditStoragePolicy{}, fmt.Errorf("audit.storage.maintenance_batch_rows must be positive")
		}
		policy.MaintenanceBatchRows = *raw.MaintenanceBatchRows
	}
	if raw.CompactAfterMaintenance != nil {
		policy.CompactAfterMaintenance = *raw.CompactAfterMaintenance
	}
	if raw.FullDetailRetention != nil {
		policy.FullDetailRetention, err = parseAuditStorageDuration(
			"full_detail_retention",
			*raw.FullDetailRetention,
		)
		if err != nil {
			return AuditStoragePolicy{}, err
		}
	}
	if raw.SummaryRetention != nil {
		policy.SummaryRetention, err = parseAuditStorageDuration(
			"summary_retention",
			*raw.SummaryRetention,
		)
		if err != nil {
			return AuditStoragePolicy{}, err
		}
	}
	if policy.SummaryRetention < policy.FullDetailRetention {
		return AuditStoragePolicy{}, fmt.Errorf(
			"audit.storage.summary_retention must be at least full_detail_retention",
		)
	}

	applyAuditStorageDetailOverrides(&policy.Detail, raw.Detail)
	return policy, nil
}

func auditStorageProfilePolicy(profile AuditStorageProfile) (AuditStoragePolicy, error) {
	detailEnabled := AuditStorageDetailPolicy{
		WireInput:           true,
		NormalizedInput:     true,
		ProviderEvidence:    true,
		EnvironmentEvidence: true,
		EvaluationContent:   true,
	}
	policy := AuditStoragePolicy{
		Profile:                 profile,
		MaintenanceInterval:     defaultAuditMaintenanceInterval,
		MaxSizeBytes:            0,
		MaintenanceBatchRows:    defaultAuditMaintenanceBatchRows,
		CompactAfterMaintenance: true,
		FullDetailRetention:     0,
		SummaryRetention:        defaultAuditSummaryRetention,
		Detail:                  detailEnabled,
	}
	switch profile {
	case AuditStorageProfileBalanced:
		policy.FullDetailRetention = defaultAuditBalancedRetention
	case AuditStorageProfileFull:
		policy.FullDetailRetention = defaultAuditFullRetention
	case AuditStorageProfileMinimal:
		policy.FullDetailRetention = 0
		policy.Detail = AuditStorageDetailPolicy{
			WireInput:           false,
			NormalizedInput:     false,
			ProviderEvidence:    false,
			EnvironmentEvidence: false,
			EvaluationContent:   false,
		}
	default:
		return AuditStoragePolicy{}, fmt.Errorf(
			"audit.storage.profile: expected %q, %q, or %q, got %q",
			AuditStorageProfileBalanced,
			AuditStorageProfileFull,
			AuditStorageProfileMinimal,
			profile,
		)
	}
	return policy, nil
}

func parseAuditStorageDuration(field string, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		slog.Warn("config audit storage duration parse failed", "field", field, "value", value, "err", err)
		return 0, fmt.Errorf("audit.storage.%s %q: %w", field, value, err)
	}
	if duration <= 0 {
		slog.Warn("config audit storage duration rejected", "field", field, "value", value)
		return 0, fmt.Errorf("audit.storage.%s must be positive", field)
	}
	return duration, nil
}

func applyAuditStorageDetailOverrides(
	policy *AuditStorageDetailPolicy,
	raw AuditStorageDetail,
) {
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

func safeDegradedAuditStoragePolicy() AuditStoragePolicy {
	return AuditStoragePolicy{
		Profile:                 AuditStorageProfileFull,
		MaintenanceInterval:     0,
		MaxSizeBytes:            0,
		MaintenanceBatchRows:    defaultAuditMaintenanceBatchRows,
		CompactAfterMaintenance: false,
		FullDetailRetention:     defaultAuditFullRetention,
		SummaryRetention:        defaultAuditSummaryRetention,
		Detail: AuditStorageDetailPolicy{
			WireInput:           true,
			NormalizedInput:     true,
			ProviderEvidence:    true,
			EnvironmentEvidence: true,
			EvaluationContent:   true,
		},
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
