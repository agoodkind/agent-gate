package config

import (
	"fmt"
	"log/slog"
	"math/big"
	"path/filepath"
	"regexp"
	"strings"
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
	defaultAuditMaxBytes         int64               = 5_000_000_000
)

var auditStorageSizePattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)(B|KB|MB|GB|TB|PB|EB|KIB|MIB|GIB|TIB|PIB|EIB)$`)

// AuditStorageProfile selects the baseline detail policy.
type AuditStorageProfile string

// AuditStorage holds raw values from the [audit.storage] TOML table.
type AuditStorage struct {
	Profile          string             `toml:"profile"`
	BucketInterval   *string            `toml:"bucket_interval"`
	RetentionBuckets *int               `toml:"retention_buckets"`
	MaxSize          *string            `toml:"max_size"`
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
	MaxBytes         int64
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
	policy.MaxBytes = defaultAuditMaxBytes
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
	if raw.MaxSize != nil {
		maxBytes, err := parseAuditStorageSize(*raw.MaxSize)
		if err != nil {
			return AuditStoragePolicy{}, fmt.Errorf("audit.storage.max_size: %w", err)
		}
		policy.MaxBytes = maxBytes
	}
	if err := policy.Rotation().Validate(); err != nil {
		slog.Warn("invalid audit rotation policy", "err", err)
		return AuditStoragePolicy{}, fmt.Errorf("audit.storage: %w", err)
	}
	applyAuditStorageDetailOverrides(&policy.Detail, raw.Detail)
	return policy, nil
}

func parseAuditStorageSize(value string) (int64, error) {
	parts := auditStorageSizePattern.FindStringSubmatch(strings.ToUpper(value))
	if parts == nil {
		return 0, fmt.Errorf("expected a nonnegative size with a byte unit, such as 5GB or 4MiB, got %q", value)
	}
	amount, valid := new(big.Rat).SetString(parts[1])
	if !valid {
		return 0, fmt.Errorf("invalid size %q", value)
	}
	unitBytes := map[string]int64{
		"B":  1,
		"KB": 1_000, "MB": 1_000_000, "GB": 1_000_000_000,
		"TB": 1_000_000_000_000, "PB": 1_000_000_000_000_000, "EB": 1_000_000_000_000_000_000,
		"KIB": 1_024, "MIB": 1_048_576, "GIB": 1_073_741_824,
		"TIB": 1_099_511_627_776, "PIB": 1_125_899_906_842_624, "EIB": 1_152_921_504_606_846_976,
	}
	amount.Mul(amount, new(big.Rat).SetInt64(unitBytes[parts[2]]))
	if !amount.IsInt() {
		return 0, fmt.Errorf("size %q must resolve to a whole number of bytes", value)
	}
	if !amount.Num().IsInt64() {
		return 0, fmt.Errorf("size %q exceeds the maximum byte count", value)
	}
	return amount.Num().Int64(), nil
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
	return auditstorage.RotationPolicy{
		Interval: policy.BucketInterval,
		Retained: policy.RetentionBuckets,
		MaxBytes: policy.MaxBytes,
	}
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

// PrepareAuditStorage validates storage for configurations constructed in memory.
func (c *Config) PrepareAuditStorage() error {
	if c == nil || c.Unusable() {
		return fmt.Errorf("audit storage requires a usable configuration")
	}
	policy, err := resolveAuditStorage(c.Audit.Storage)
	if err != nil {
		return err
	}
	c.auditStoragePolicy = policy
	return nil
}
