package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

func TestSetupNonInteractiveFreshInstallDoesNotCreateAuditDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	binPath := writeSetupExecutable(t)
	dependencies := Dependencies{
		PrepareInstallation: func(options installer.InstallationOptions) (*installer.InstallationPlan, error) {
			configPlan, err := config.PrepareDefaults(*options.Config)
			if err != nil {
				return nil, err
			}
			return &installer.InstallationPlan{Config: configPlan}, nil
		},
	}
	plan, err := Prepare(t.Context(), Options{
		BinPath: binPath, Providers: []installer.Provider{installer.ProviderClaude},
		AuditProfile: config.AuditStorageProfileFull, AutoUpdate: config.UpdateModeApply,
	}, dependencies)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = plan.Close() })
	if _, err := os.Stat(config.DefaultAuditSQLitePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit database exists after prepare: %v", err)
	}
}

func TestSetupPrepareGeneratesVerificationIDBeforeInstallation(t *testing.T) {
	prepareCalls := 0
	_, err := Prepare(t.Context(), Options{
		BinPath:      writeSetupExecutable(t),
		Providers:    []installer.Provider{installer.ProviderCodex},
		AuditProfile: config.AuditStorageProfileFull,
		AutoUpdate:   config.UpdateModeApply,
	}, Dependencies{
		NewSetupID: func() (string, error) { return "", errors.New("entropy unavailable") },
		PrepareInstallation: func(installer.InstallationOptions) (*installer.InstallationPlan, error) {
			prepareCalls++
			return nil, nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "entropy unavailable") {
		t.Fatalf("Prepare error = %v", err)
	}
	if prepareCalls != 0 {
		t.Fatalf("installation preparation calls = %d, want 0", prepareCalls)
	}
}

func TestSetupNonInteractiveRejectsEmptyProviderSelection(t *testing.T) {
	for _, providers := range [][]installer.Provider{nil, {}} {
		prepareCalls := 0
		_, err := Prepare(t.Context(), Options{
			BinPath: "unused", Providers: providers,
			AuditProfile: config.AuditStorageProfileFull, AutoUpdate: config.UpdateModeApply,
		}, Dependencies{PrepareInstallation: func(installer.InstallationOptions) (*installer.InstallationPlan, error) {
			prepareCalls++
			return nil, nil
		}})
		if err == nil || err.Error() != "at least one provider is required" {
			t.Fatalf("Prepare providers %#v error = %v", providers, err)
		}
		if prepareCalls != 0 {
			t.Fatalf("prepare calls = %d, want 0", prepareCalls)
		}
	}
}

func TestSetupApplyInstallsThenVerifiesSelectedProviders(t *testing.T) {
	installation := &installer.InstallationPlan{}
	cfg := &config.Config{}
	plan := &Plan{
		Installation: installation,
		Providers:    []installer.Provider{installer.ProviderCursor},
		binPath:      "/prepared/agent-gate",
		probeConfig:  cfg,
		setupID:      "setup-48",
	}
	applyCalls := 0
	verifyCalls := 0
	result, err := Apply(t.Context(), plan, Dependencies{
		ApplyInstallation: func(received *installer.InstallationPlan) (installer.ApplyResult, error) {
			applyCalls++
			if received != installation {
				t.Fatal("apply received a different installation plan")
			}
			return installer.ApplyResult{}, nil
		},
		VerifyInstalledHooks: func(_ context.Context, request ProbeRequest) ([]ProbeResult, error) {
			verifyCalls++
			if applyCalls != 1 {
				t.Fatal("verification ran before installation")
			}
			if request.SetupID != "setup-48" || request.BinPath != "/prepared/agent-gate" || request.Config != cfg {
				t.Fatalf("probe request = %#v", request)
			}
			return []ProbeResult{{Provider: installer.ProviderCursor, Decision: "allow"}}, nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applyCalls != 1 || verifyCalls != 1 {
		t.Fatalf("apply calls = %d, verify calls = %d", applyCalls, verifyCalls)
	}
	if result.SetupID != "setup-48" || len(result.Probes) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestSetupApplyUsesRetainedInstallationAndProviders(t *testing.T) {
	retainedInstallation := &installer.InstallationPlan{}
	appliedConfig := &config.Config{}
	plan := &Plan{
		Installation: &installer.InstallationPlan{},
		Providers:    []installer.Provider{installer.ProviderClaude},
		binPath:      "/prepared/agent-gate",
		probeConfig:  appliedConfig,
		setupID:      "setup-retained",
		installation: retainedInstallation,
		providers:    []installer.Provider{installer.ProviderGemini},
		prepared:     true,
	}
	_, err := Apply(t.Context(), plan, Dependencies{
		ApplyInstallation: func(received *installer.InstallationPlan) (installer.ApplyResult, error) {
			if received != retainedInstallation {
				t.Fatal("apply used the exported installation snapshot")
			}
			return installer.ApplyResult{}, nil
		},
		VerifyInstalledHooks: func(_ context.Context, request ProbeRequest) ([]ProbeResult, error) {
			if len(request.Providers) != 1 || request.Providers[0] != installer.ProviderGemini {
				t.Fatalf("providers = %#v, want retained Gemini", request.Providers)
			}
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func writeSetupExecutable(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-gate")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile executable: %v", err)
	}
	return path
}
