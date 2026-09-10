package config_test

import (
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
	if policy.Profile != config.AuditStorageProfileFull || policy.BucketInterval != 24*time.Hour || policy.RetentionBuckets != 7 {
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
[audit.storage.detail]
wire_input = false
normalized_input = false
provider_evidence = false
environment_evidence = false
evaluation_content = false
`)
	policy := cfg.AuditStoragePolicy()
	if policy.Rotation().Interval != 12*time.Hour || policy.Rotation().Retained != 3 {
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
		`bucket_interval = "999999999999h"`, `retention_buckets = 0`,
		`retention_buckets = -1`, `retention_buckets = 9223372036854775807`,
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
