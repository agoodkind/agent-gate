package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

func TestSetupNonInteractivePreviewsBeforeWrites(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	binPath := writeSetupExecutable(t)
	databasePath := config.DefaultAuditSQLitePath()
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll database directory: %v", err)
	}
	if err := os.WriteFile(databasePath, []byte("existing"), 0o600); err != nil {
		t.Fatalf("WriteFile database: %v", err)
	}

	mutated := false
	previewed := false
	dependencies := Dependencies{
		PrepareInstallation: func(options installer.InstallationOptions) (*installer.InstallationPlan, error) {
			if options.Config == nil || options.Hooks == nil || options.Service == nil {
				t.Fatal("installation options are incomplete")
			}
			configPlan, err := config.PrepareDefaults(*options.Config)
			if err != nil {
				return nil, err
			}
			return &installer.InstallationPlan{Config: configPlan}, nil
		},
		Preview: func(
			_ context.Context,
			path string,
			policy config.AuditStoragePolicy,
			_ time.Time,
		) (auditmaintenance.Plan, error) {
			if mutated {
				t.Fatal("preview ran after a mutation")
			}
			if _, err := os.Stat(config.Path()); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("config exists before apply: %v", err)
			}
			if path != databasePath {
				t.Fatalf("database path = %q, want %q", path, databasePath)
			}
			if policy.Profile != config.AuditStorageProfileMinimal {
				t.Fatalf("profile = %q, want minimal", policy.Profile)
			}
			previewed = true
			return auditmaintenance.Plan{EstimatedDeleteBytes: 41}, nil
		},
		ApplyInstallation: func(*installer.InstallationPlan) (installer.ApplyResult, error) {
			mutated = true
			return installer.ApplyResult{}, nil
		},
	}
	plan, err := Prepare(t.Context(), Options{
		BinPath: binPath, Providers: []installer.Provider{installer.ProviderCodex},
		AuditProfile: config.AuditStorageProfileMinimal, AutoUpdate: config.UpdateModeCheck,
	}, dependencies)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = plan.Close() })
	if !previewed {
		t.Fatal("maintenance preview did not run")
	}
	if mutated {
		t.Fatal("prepare mutated installation state")
	}
	if plan.Maintenance == nil || plan.Maintenance.EstimatedDeleteBytes != 41 {
		t.Fatalf("maintenance = %#v", plan.Maintenance)
	}
}

func TestSetupNonInteractiveFreshInstallDoesNotCreateAuditDatabase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	binPath := writeSetupExecutable(t)
	previewCalls := 0
	dependencies := Dependencies{
		PrepareInstallation: func(options installer.InstallationOptions) (*installer.InstallationPlan, error) {
			configPlan, err := config.PrepareDefaults(*options.Config)
			if err != nil {
				return nil, err
			}
			return &installer.InstallationPlan{Config: configPlan}, nil
		},
		Preview: func(context.Context, string, config.AuditStoragePolicy, time.Time) (auditmaintenance.Plan, error) {
			previewCalls++
			return auditmaintenance.Plan{}, nil
		},
	}
	plan, err := Prepare(t.Context(), Options{
		BinPath: binPath, Providers: []installer.Provider{installer.ProviderClaude},
		AuditProfile: config.AuditStorageProfileBalanced, AutoUpdate: config.UpdateModeApply,
	}, dependencies)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = plan.Close() })
	if previewCalls != 0 {
		t.Fatalf("preview calls = %d, want 0", previewCalls)
	}
	if plan.Maintenance != nil {
		t.Fatalf("maintenance = %#v, want nil", plan.Maintenance)
	}
	if _, err := os.Stat(config.DefaultAuditSQLitePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audit database exists after prepare: %v", err)
	}
}

func TestSetupNonInteractiveRejectsBrokenAuditDatabaseSymlink(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	binPath := writeSetupExecutable(t)
	databasePath := config.DefaultAuditSQLitePath()
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatalf("MkdirAll database directory: %v", err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing.db"), databasePath); err != nil {
		t.Fatalf("Symlink database: %v", err)
	}
	previewCalls := 0
	_, err := Prepare(t.Context(), Options{
		BinPath: binPath, Providers: []installer.Provider{installer.ProviderClaude},
		AuditProfile: config.AuditStorageProfileBalanced, AutoUpdate: config.UpdateModeApply,
	}, Dependencies{
		PrepareInstallation: func(options installer.InstallationOptions) (*installer.InstallationPlan, error) {
			configPlan, prepareErr := config.PrepareDefaults(*options.Config)
			if prepareErr != nil {
				return nil, prepareErr
			}
			return &installer.InstallationPlan{Config: configPlan}, nil
		},
		Preview: func(context.Context, string, config.AuditStoragePolicy, time.Time) (auditmaintenance.Plan, error) {
			previewCalls++
			return auditmaintenance.Plan{}, nil
		},
	})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Prepare error = %v, want broken database path error", err)
	}
	if previewCalls != 0 {
		t.Fatalf("preview calls = %d, want 0", previewCalls)
	}
	info, lstatErr := os.Lstat(databasePath)
	if lstatErr != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("broken database symlink was not preserved: %#v, %v", info, lstatErr)
	}
}

func TestSetupPrepareGeneratesVerificationIDBeforeInstallation(t *testing.T) {
	prepareCalls := 0
	_, err := Prepare(t.Context(), Options{
		BinPath:      writeSetupExecutable(t),
		Providers:    []installer.Provider{installer.ProviderCodex},
		AuditProfile: config.AuditStorageProfileBalanced,
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
			AuditProfile: config.AuditStorageProfileBalanced, AutoUpdate: config.UpdateModeApply,
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
