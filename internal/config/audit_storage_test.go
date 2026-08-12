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

func TestAuditStoragePolicyDefaultsToBalanced(t *testing.T) {
	cfg := loadAuditStorageConfig(t, "[audit]\nenabled = true\n")

	policy := cfg.AuditStoragePolicy()

	if policy.Profile != config.AuditStorageProfileBalanced {
		t.Fatalf("profile = %q, want balanced", policy.Profile)
	}
	if policy.FullDetailRetention != 168*time.Hour {
		t.Fatalf("detail retention = %s, want 168h", policy.FullDetailRetention)
	}
	if policy.SummaryRetention != 720*time.Hour {
		t.Fatalf("summary retention = %s, want 720h", policy.SummaryRetention)
	}
	if policy.MaintenanceInterval != 24*time.Hour {
		t.Fatalf("maintenance interval = %s, want 24h", policy.MaintenanceInterval)
	}
	if policy.MaxSizeBytes != 0 {
		t.Fatalf("max size = %d, want disabled", policy.MaxSizeBytes)
	}
	if policy.MaintenanceBatchRows != 1000 {
		t.Fatalf("maintenance batch rows = %d, want 1000", policy.MaintenanceBatchRows)
	}
	if !policy.CompactAfterMaintenance {
		t.Fatal("compact after maintenance = false, want true")
	}
	assertAuditStorageDetail(t, policy.Detail, true)
}

func TestAuditStorageProfiles(t *testing.T) {
	testCases := []struct {
		name            string
		profile         config.AuditStorageProfile
		detailRetention time.Duration
		detailEnabled   bool
	}{
		{
			name:            "balanced",
			profile:         config.AuditStorageProfileBalanced,
			detailRetention: 168 * time.Hour,
			detailEnabled:   true,
		},
		{
			name:            "full",
			profile:         config.AuditStorageProfileFull,
			detailRetention: 720 * time.Hour,
			detailEnabled:   true,
		},
		{
			name:            "minimal",
			profile:         config.AuditStorageProfileMinimal,
			detailRetention: 0,
			detailEnabled:   false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := loadAuditStorageConfig(t, "[audit.storage]\nprofile = \""+testCase.name+"\"\n")

			policy := cfg.AuditStoragePolicy()

			if policy.Profile != testCase.profile {
				t.Fatalf("profile = %q, want %q", policy.Profile, testCase.profile)
			}
			if policy.FullDetailRetention != testCase.detailRetention {
				t.Fatalf("detail retention = %s, want %s", policy.FullDetailRetention, testCase.detailRetention)
			}
			if policy.SummaryRetention != 720*time.Hour {
				t.Fatalf("summary retention = %s, want 720h", policy.SummaryRetention)
			}
			assertAuditStorageDetail(t, policy.Detail, testCase.detailEnabled)
		})
	}
}

func TestAuditStoragePolicyAppliesEveryOverride(t *testing.T) {
	cfg := loadAuditStorageConfig(t, `
[audit.storage]
profile = "balanced"
maintenance_interval = "12h"
max_size_mb = 25
maintenance_batch_rows = 123
compact_after_maintenance = false
full_detail_retention = "24h"
summary_retention = "48h"

[audit.storage.detail]
wire_input = false
normalized_input = false
provider_evidence = false
environment_evidence = false
evaluation_content = false
`)

	policy := cfg.AuditStoragePolicy()

	if policy.MaintenanceInterval != 12*time.Hour {
		t.Fatalf("maintenance interval = %s, want 12h", policy.MaintenanceInterval)
	}
	if policy.MaxSizeBytes != 25_000_000 {
		t.Fatalf("max size = %d, want %d", policy.MaxSizeBytes, 25_000_000)
	}
	if policy.MaintenanceBatchRows != 123 {
		t.Fatalf("maintenance batch rows = %d, want 123", policy.MaintenanceBatchRows)
	}
	if policy.CompactAfterMaintenance {
		t.Fatal("compact after maintenance = true, want explicit false")
	}
	if policy.FullDetailRetention != 24*time.Hour {
		t.Fatalf("detail retention = %s, want 24h", policy.FullDetailRetention)
	}
	if policy.SummaryRetention != 48*time.Hour {
		t.Fatalf("summary retention = %s, want 48h", policy.SummaryRetention)
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

func TestAuditStorageMaxSizeZeroDisablesSizeTarget(t *testing.T) {
	cfg := loadAuditStorageConfig(t, "[audit.storage]\nmax_size_mb = 0\n")

	if size := cfg.AuditStoragePolicy().MaxSizeBytes; size != 0 {
		t.Fatalf("max size = %d, want disabled", size)
	}
}

func TestAuditStorageRejectsInvalidValues(t *testing.T) {
	testCases := []struct {
		name    string
		body    string
		message string
	}{
		{name: "negative size", body: "max_size_mb = -1", message: "max_size_mb"},
		{name: "unknown profile", body: `profile = "archive"`, message: "profile"},
		{name: "zero batch", body: "maintenance_batch_rows = 0", message: "maintenance_batch_rows"},
		{name: "negative batch", body: "maintenance_batch_rows = -1", message: "maintenance_batch_rows"},
		{name: "zero interval", body: `maintenance_interval = "0s"`, message: "maintenance_interval"},
		{name: "negative interval", body: `maintenance_interval = "-1h"`, message: "maintenance_interval"},
		{name: "invalid interval", body: `maintenance_interval = "daily"`, message: "maintenance_interval"},
		{name: "zero detail duration", body: `full_detail_retention = "0s"`, message: "full_detail_retention"},
		{name: "negative detail duration", body: `full_detail_retention = "-1h"`, message: "full_detail_retention"},
		{name: "zero summary duration", body: `summary_retention = "0s"`, message: "summary_retention"},
		{name: "negative summary duration", body: `summary_retention = "-1h"`, message: "summary_retention"},
		{name: "summary shorter than detail", body: `full_detail_retention = "48h"` + "\n" + `summary_retention = "24h"`, message: "summary_retention"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			body := "[audit.storage]\n" + testCase.body + "\n"
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			_, err := config.LoadExisting(path)
			if err == nil {
				t.Fatal("LoadExisting accepted invalid audit storage")
			}
			if !strings.Contains(err.Error(), testCase.message) {
				t.Fatalf("LoadExisting error = %q, want %q", err, testCase.message)
			}
		})
	}
}

func TestAuditStorageDegradedLoadRetainsDetailAndDisablesMaintenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[audit.storage]\nmax_size_mb = -1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatalf("LoadDegradedPath: %v", err)
	}

	policy := cfg.AuditStoragePolicy()
	if policy.MaintenanceInterval != 0 {
		t.Fatalf("maintenance interval = %s, want disabled", policy.MaintenanceInterval)
	}
	assertAuditStorageDetail(t, policy.Detail, true)
	failures := cfg.Failures()
	if len(failures) != 1 {
		t.Fatalf("failures = %v, want one audit storage failure", failures)
	}
	if failures[0].Kind != config.LoadFailureSection || failures[0].Scope != "audit.storage" {
		t.Fatalf("failure = %+v, want audit.storage section", failures[0])
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
