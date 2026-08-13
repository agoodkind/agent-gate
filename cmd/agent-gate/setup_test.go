package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/setup"
)

func TestSetupNonInteractivePreviewsBeforeWrites(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	applyCalls := 0
	exitCode := runSetupWithDependencies(
		[]string{"--non-interactive", "--providers", "codex", "--audit-profile", "minimal", "--auto-update", "check"},
		&stdout,
		&stderr,
		setupCommandDependencies{
			ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
			Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
				return &setup.Plan{
					Providers:       []installer.Provider{installer.ProviderCodex},
					EffectivePolicy: config.AuditStoragePolicy{Profile: config.AuditStorageProfileMinimal},
					Maintenance:     &auditmaintenance.Plan{EstimatedDeleteBytes: 37},
				}, nil
			},
			Apply: func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error) {
				applyCalls++
				if !strings.Contains(stdout.String(), "estimated delete bytes: 37") {
					t.Fatalf("stdout before apply = %q", stdout.String())
				}
				return setup.Result{SetupID: "setup-48"}, nil
			},
		},
	)
	if exitCode != 0 || applyCalls != 1 {
		t.Fatalf("exit code = %d, apply calls = %d, stderr = %q", exitCode, applyCalls, stderr.String())
	}
}

func TestSetupNonInteractiveRequiresProviderSelection(t *testing.T) {
	exitCode, stderr := runSetupError(t, []string{
		"--non-interactive", "--audit-profile", "balanced", "--auto-update", "apply",
	})
	if exitCode != 2 || !strings.Contains(stderr, "--providers is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveRejectsEmptyProviderSelection(t *testing.T) {
	exitCode, stderr := runSetupError(t, []string{
		"--non-interactive", "--providers", "", "--audit-profile", "balanced", "--auto-update", "apply",
	})
	if exitCode != 2 || !strings.Contains(stderr, "at least one provider is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveRequiresAuditProfile(t *testing.T) {
	exitCode, stderr := runSetupError(t, []string{
		"--non-interactive", "--providers", "claude", "--auto-update", "apply",
	})
	if exitCode != 2 || !strings.Contains(stderr, "--audit-profile is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveRequiresAutoUpdateMode(t *testing.T) {
	exitCode, stderr := runSetupError(t, []string{
		"--non-interactive", "--providers", "claude", "--audit-profile", "balanced",
	})
	if exitCode != 2 || !strings.Contains(stderr, "--auto-update is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveReturnsTwoForPreflightFailure(t *testing.T) {
	exitCode, stderr := runSetupFailure(t, errors.New("invalid existing hooks"), nil)
	if exitCode != 2 || !strings.Contains(stderr, "preflight: invalid existing hooks") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveReturnsOneForApplyFailure(t *testing.T) {
	exitCode, stderr := runSetupFailure(t, nil, errors.New("service apply failed"))
	if exitCode != 1 || !strings.Contains(stderr, "service apply failed") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveReturnsOneForProbeFailure(t *testing.T) {
	exitCode, stderr := runSetupFailure(t, nil, errors.New("codex: durable audit timed out"))
	if exitCode != 1 || !strings.Contains(stderr, "codex: durable audit timed out") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractivePrintsRepairCommand(t *testing.T) {
	applyErr := &installer.ApplyError{
		Stage: installer.StageHooks, RepairCommand: "agent-gate install hooks --providers codex",
		Err: errors.New("write hooks"),
	}
	exitCode, stderr := runSetupFailure(t, nil, applyErr)
	if exitCode != 1 || !strings.Contains(stderr, applyErr.RepairCommand) {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr)
	}
}

func TestSetupNonInteractiveJSONOutputIsMachineReadable(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runSetupWithDependencies(
		[]string{"--non-interactive", "--providers", "claude", "--audit-profile", "balanced", "--auto-update", "apply", "--json"},
		&stdout,
		&stderr,
		setupCommandDependencies{
			ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
			Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
				return &setup.Plan{
					Providers:       []installer.Provider{installer.ProviderClaude},
					EffectivePolicy: config.AuditStoragePolicy{Profile: config.AuditStorageProfileBalanced},
				}, nil
			},
			Apply: func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error) {
				return setup.Result{SetupID: "setup-json"}, nil
			},
		},
	)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for _, want := range []string{`"phase":"preview"`, `"existing_records":0`, `"phase":"complete"`, `"setup_id":"setup-json"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func runSetupError(t *testing.T, args []string) (int, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runSetupWithDependencies(args, &stdout, &stderr, setupCommandDependencies{
		ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
		Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
			t.Fatal("prepare called")
			return nil, nil
		},
	})
	return exitCode, stderr.String()
}

func runSetupFailure(t *testing.T, prepareErr error, applyErr error) (int, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runSetupWithDependencies(
		[]string{"--non-interactive", "--providers", "codex", "--audit-profile", "balanced", "--auto-update", "apply"},
		&stdout,
		&stderr,
		setupCommandDependencies{
			ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
			Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
				if prepareErr != nil {
					return nil, prepareErr
				}
				return &setup.Plan{EffectivePolicy: config.AuditStoragePolicy{}}, nil
			},
			Apply: func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error) {
				return setup.Result{}, applyErr
			},
		},
	)
	return exitCode, stderr.String()
}
