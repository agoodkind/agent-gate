package main

import (
	"bytes"
	"context"
	"errors"
	"reflect"
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

func TestSetupInteractivePreviewsConfirmsAppliesAndPrintsEveryProvider(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	prompter := &scriptedSetupPrompter{
		providers: []installer.Provider{installer.ProviderClaude, installer.ProviderCursor},
		profile:   config.AuditStorageProfileMinimal,
		confirm: func(summary PlanSummary) (bool, error) {
			if !strings.Contains(stdout.String(), "estimated delete bytes: 41") {
				t.Fatalf("stdout before confirmation = %q", stdout.String())
			}
			return true, nil
		},
	}
	exitCode := runSetupWithDependencies(nil, &stdout, &stderr, setupCommandDependencies{
		ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
		DetectProviders: func(setup.DetectOptions) ([]setup.ProviderState, error) {
			return []setup.ProviderState{{Provider: installer.ProviderClaude, ClientPath: "/bin/claude"}}, nil
		},
		LoadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		Prompter:   prompter,
		Prepare: func(_ context.Context, options setup.Options, _ setup.Dependencies) (*setup.Plan, error) {
			if !reflect.DeepEqual(options.Providers, prompter.providers) {
				t.Fatalf("providers = %#v", options.Providers)
			}
			if options.AuditProfile != prompter.profile || options.AutoUpdate != "" {
				t.Fatalf("options = %#v", options)
			}
			return &setup.Plan{
				Providers:       append([]installer.Provider(nil), options.Providers...),
				EffectivePolicy: config.AuditStoragePolicy{Profile: options.AuditProfile},
				Maintenance:     &auditmaintenance.Plan{EstimatedDeleteBytes: 41},
			}, nil
		},
		Apply: func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error) {
			return setup.Result{SetupID: "setup-interactive", Probes: []setup.ProbeResult{
				{Provider: installer.ProviderClaude, ReceiptID: 1, EvaluationID: "eval-1", Decision: "allow"},
				{Provider: installer.ProviderCursor, ReceiptID: 2, EvaluationID: "eval-2", Decision: "allow"},
			}}, nil
		},
	})
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	for _, want := range []string{"verified claude", "verified cursor"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), want)
		}
	}
}

func TestSetupInteractiveCancellationClosesPlanWithoutApply(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	closeCalls := 0
	exitCode := runSetupWithDependencies(nil, &stdout, &stderr, setupCommandDependencies{
		ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
		DetectProviders: func(setup.DetectOptions) ([]setup.ProviderState, error) {
			return nil, nil
		},
		LoadConfig: func() (*config.Config, error) { return &config.Config{}, nil },
		Prompter: &scriptedSetupPrompter{
			providers: []installer.Provider{installer.ProviderCodex},
			profile:   config.AuditStorageProfileBalanced,
			confirm:   func(PlanSummary) (bool, error) { return false, nil },
		},
		Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
			return &setup.Plan{}, nil
		},
		Close: func(*setup.Plan) error {
			closeCalls++
			return nil
		},
		Apply: func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error) {
			t.Fatal("Apply called after cancellation")
			return setup.Result{}, nil
		},
	})
	if exitCode != 0 || closeCalls != 1 || !strings.Contains(stdout.String(), "setup cancelled") {
		t.Fatalf("exit code = %d, close calls = %d, stdout = %q, stderr = %q", exitCode, closeCalls, stdout.String(), stderr.String())
	}
}

func TestSetupInteractiveRejectsEmptySelectionBeforePrepare(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := runSetupWithDependencies(nil, &stdout, &stderr, setupCommandDependencies{
		ResolveExecutable: func() (string, error) { return "/bin/echo", nil },
		DetectProviders:   func(setup.DetectOptions) ([]setup.ProviderState, error) { return nil, nil },
		LoadConfig:        func() (*config.Config, error) { return &config.Config{}, nil },
		Prompter: &scriptedSetupPrompter{
			providers: []installer.Provider{},
			profile:   config.AuditStorageProfileBalanced,
		},
		Prepare: func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error) {
			t.Fatal("Prepare called for empty selection")
			return nil, nil
		},
	})
	if exitCode != 2 || !strings.Contains(stderr.String(), "at least one provider is required") {
		t.Fatalf("exit code = %d, stderr = %q", exitCode, stderr.String())
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

type scriptedSetupPrompter struct {
	providers []installer.Provider
	profile   config.AuditStorageProfile
	confirm   func(PlanSummary) (bool, error)
}

func (prompter *scriptedSetupPrompter) SelectProviders(
	[]setup.ProviderState,
) ([]installer.Provider, error) {
	return append([]installer.Provider(nil), prompter.providers...), nil
}

func (prompter *scriptedSetupPrompter) SelectAuditProfile(
	config.AuditStoragePolicy,
) (config.AuditStorageProfile, error) {
	return prompter.profile, nil
}

func (prompter *scriptedSetupPrompter) Confirm(summary PlanSummary) (bool, error) {
	if prompter.confirm == nil {
		return true, nil
	}
	return prompter.confirm(summary)
}
