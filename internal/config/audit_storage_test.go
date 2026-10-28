package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

func loadAuditStorageConfig(t *testing.T, body string) *config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	return cfg
}

func TestAuditStoragePolicyDefaultsToFull(t *testing.T) {
	cfg := loadAuditStorageConfig(t, "[audit]\nenabled = true\n")
	policy := cfg.AuditStoragePolicy()
	if policy.Profile != config.AuditStorageProfileFull || policy.BucketInterval != 24*time.Hour || policy.RetentionBuckets != 7 || policy.MaxBytes != 5_000_000_000 {
		t.Fatalf("unexpected defaults: %+v", policy)
	}
	assertAuditStorageDetail(t, policy.Detail, true)
}

func TestAuditStorageProfiles(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		t.Run(profile, func(t *testing.T) {
			cfg := loadAuditStorageConfig(t, "[audit.storage]\nprofile = \""+profile+"\"\n")
			assertAuditStorageDetail(t, cfg.AuditStoragePolicy().Detail, profile == "full")
		})
	}
}

func TestAuditStoragePolicyAppliesEveryOverride(t *testing.T) {
	cfg := loadAuditStorageConfig(t, `
[audit.storage]
profile = "full"
bucket_interval = "12h"
retention_buckets = 3
max_size = "2GiB"
[audit.storage.detail]
wire_input = false
normalized_input = false
provider_evidence = false
environment_evidence = false
evaluation_content = false
`)
	policy := cfg.AuditStoragePolicy()
	if policy.Rotation().Interval != 12*time.Hour || policy.Rotation().Retained != 3 || policy.Rotation().MaxBytes != 2_147_483_648 {
		t.Fatalf("policy = %+v", policy)
	}
	assertAuditStorageDetail(t, policy.Detail, false)
}

func TestAuditStoragePolicyAllowsMinimalDetailOverrides(t *testing.T) {
	cfg := loadAuditStorageConfig(t, `
[audit.storage]
profile = "minimal"
[audit.storage.detail]
wire_input = true
normalized_input = true
provider_evidence = true
environment_evidence = true
evaluation_content = true
`)
	assertAuditStorageDetail(t, cfg.AuditStoragePolicy().Detail, true)
}

func TestAuditStorageRejectsInvalidValues(t *testing.T) {
	for _, body := range []string{
		`profile = "balanced"`, `profile = "archive"`,
		`bucket_interval = "0s"`, `bucket_interval = "-1h"`,
		`bucket_interval = "daily"`, `bucket_interval = "1.5s"`,
		`bucket_interval = "999999999999h"`,
		`retention_buckets = -1`, `retention_buckets = 9223372036854775807`,
		"retention_buckets = 0\nmax_size = \"0B\"",
		`max_size = "-1GB"`, `max_size = "+5GB"`, `max_size = "5"`,
		`max_size = ""`, `max_size = "5 GB"`, `max_size = "1e3MB"`,
		`max_size = "5XB"`, `max_size = "4MB"`, `max_size = "4194303B"`,
		`max_size = "0.5B"`, `max_size = "4.0000001MiB"`,
		`max_size = "9223372036854775808B"`, `max_size = "10EB"`,
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("[audit.storage]\n"+body+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			for _, loader := range []func(string) (*config.Config, error){config.LoadExisting, config.LoadDegradedPath} {
				_, err := loader(path)
				if err == nil || !strings.Contains(err.Error(), "audit.storage") {
					t.Fatalf("invalid storage accepted: %v", err)
				}
			}
		})
	}
}

func TestAuditStorageAcceptsByteSizes(t *testing.T) {
	for _, testCase := range []struct {
		value string
		bytes int64
	}{
		{"0B", 0},
		{"4194304B", 4_194_304},
		{"5000KB", 5_000_000},
		{"5MB", 5_000_000},
		{"5GB", 5_000_000_000},
		{"1TB", 1_000_000_000_000},
		{"1PB", 1_000_000_000_000_000},
		{"1EB", 1_000_000_000_000_000_000},
		{"4096KiB", 4_194_304},
		{"4MiB", 4_194_304},
		{"4.5MiB", 4_718_592},
		{"1GiB", 1_073_741_824},
		{"1TiB", 1_099_511_627_776},
		{"1PiB", 1_125_899_906_842_624},
		{"1EiB", 1_152_921_504_606_846_976},
		{"5gb", 5_000_000_000},
		{"9223372036854775807B", 9_223_372_036_854_775_807},
	} {
		t.Run(testCase.value, func(t *testing.T) {
			cfg := loadAuditStorageConfig(t, fmt.Sprintf("[audit.storage]\nmax_size = %q\n", testCase.value))
			policy := cfg.AuditStoragePolicy()
			if policy.MaxBytes != testCase.bytes || policy.Rotation().MaxBytes != testCase.bytes {
				t.Fatalf("policy = %+v, want %d bytes", policy, testCase.bytes)
			}
		})
	}
}

func TestAuditStorageAllowsSizeOnlyRetention(t *testing.T) {
	cfg := loadAuditStorageConfig(t, "[audit.storage]\nretention_buckets = 0\n")
	policy := cfg.AuditStoragePolicy()
	if policy.RetentionBuckets != 0 || policy.MaxBytes != 5_000_000_000 {
		t.Fatalf("policy = %+v", policy)
	}
}

func TestAuditStorageDefaultsPreserveRetentionSettings(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		settings string
		retained int
		maxBytes int64
	}{
		{"size only", "retention_buckets = 0\nmax_size = \"2GiB\"\n", 0, 2_147_483_648},
		{"age only", "retention_buckets = 3\nmax_size = \"0B\"\n", 3, 0},
		{"both", "retention_buckets = 3\nmax_size = \"1GB\"\n", 3, 1_000_000_000},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := config.Path()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			contents := "[audit.storage]\nprofile = \"full\"\n" + testCase.settings
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			plan, err := config.PrepareDefaults(config.EnsureDefaultsOptions{
				AuditProfile: config.AuditStorageProfileMinimal,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = plan.Close() })
			if _, err := config.ApplyDefaults(plan); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadExisting(path)
			if err != nil {
				t.Fatal(err)
			}
			policy := cfg.AuditStoragePolicy()
			if policy.Profile != config.AuditStorageProfileMinimal || policy.RetentionBuckets != testCase.retained || policy.MaxBytes != testCase.maxBytes {
				t.Fatalf("policy = %+v", policy)
			}
		})
	}
}

func TestAuditCatalogOptionsUseStableUserPaths(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(directory, "runtime"))
	cfg := loadAuditStorageConfig(t, "[audit.outputs.sqlite]\npath = \"/custom/audit.db\"\n")
	options := cfg.AuditCatalogOptions()
	if options.BasePath != "/custom/audit.db" || options.StatePath != filepath.Join(config.DefaultStateDir(), "audit-storage.json") || options.CoordinationPath != filepath.Join(filepath.Dir(config.RuntimeDir()), "agent-gate-audit.lock") {
		t.Fatalf("options = %+v", options)
	}
}

func TestUndecodableConfigCannotProvideRotationPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[audit.storage]\nbucket_interval = 12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditStoragePolicy().Rotation().Validate() == nil {
		t.Fatal("invalid document supplied destructive fallback policy")
	}
}

func assertAuditStorageDetail(t *testing.T, detail config.AuditStorageDetailPolicy, want bool) {
	t.Helper()
	if detail.WireInput != want {
		t.Fatalf("wire input = %t, want %t", detail.WireInput, want)
	}
	if detail.NormalizedInput != want {
		t.Fatalf("normalized input = %t, want %t", detail.NormalizedInput, want)
	}
	if detail.ProviderEvidence != want {
		t.Fatalf("provider evidence = %t, want %t", detail.ProviderEvidence, want)
	}
	if detail.EnvironmentEvidence != want {
		t.Fatalf("environment evidence = %t, want %t", detail.EnvironmentEvidence, want)
	}
	if detail.EvaluationContent != want {
		t.Fatalf("evaluation content = %t, want %t", detail.EvaluationContent, want)
	}
}
