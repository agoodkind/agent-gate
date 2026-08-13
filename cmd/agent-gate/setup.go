package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/setup"
)

type setupCommandDependencies struct {
	ResolveExecutable func() (string, error)
	Prepare           func(context.Context, setup.Options, setup.Dependencies) (*setup.Plan, error)
	Apply             func(context.Context, *setup.Plan, setup.Dependencies) (setup.Result, error)
	Coordinator       setup.Dependencies
}

type setupFlagValues struct {
	binPath        string
	providerNames  string
	auditProfile   string
	autoUpdate     string
	nonInteractive bool
	jsonOutput     bool
}

type setupPreviewOutput struct {
	Phase           string                    `json:"phase"`
	Providers       []installer.Provider      `json:"providers"`
	EffectivePolicy config.AuditStoragePolicy `json:"effective_policy"`
	DatabaseExists  bool                      `json:"database_exists"`
	ExistingRecords *int64                    `json:"existing_records,omitempty"`
	Maintenance     *auditmaintenance.Plan    `json:"maintenance,omitempty"`
}

type setupCompleteOutput struct {
	Phase string `json:"phase"`
	setup.Result
}

type setupErrorOutput struct {
	Phase         string `json:"phase"`
	Error         string `json:"error"`
	RepairCommand string `json:"repair_command,omitempty"`
}

func runSetup(args []string, stdout io.Writer, stderr io.Writer) int {
	return runSetupWithDependencies(args, stdout, stderr, defaultSetupCommandDependencies())
}

func runSetupWithDependencies(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	dependencies setupCommandDependencies,
) int {
	values := setupFlagValues{}
	flags := flag.NewFlagSet("agent-gate setup", flag.ContinueOnError)
	flags.SetOutput(setupFlagOutput(args, stdout, stderr))
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `Usage: agent-gate setup --non-interactive --providers LIST --audit-profile PROFILE --auto-update MODE [--bin-path PATH] [--json]

Flags:
  --non-interactive  Run setup without prompts
  --providers        Select a nonempty comma-separated provider list
  --audit-profile    Select balanced, full, or minimal
  --auto-update      Select check, apply, or off
  --bin-path         Use the installed agent-gate binary at PATH
  --json             Print machine-readable JSON lines`)
	}
	flags.BoolVar(&values.nonInteractive, "non-interactive", false, "run setup without prompts")
	flags.StringVar(&values.providerNames, "providers", "", "comma-separated providers")
	flags.StringVar(&values.auditProfile, "audit-profile", "", "audit storage profile: balanced, full, or minimal")
	flags.StringVar(&values.autoUpdate, "auto-update", "", "update mode: check, apply, or off")
	flags.StringVar(&values.binPath, "bin-path", "", "path to the installed agent-gate binary")
	flags.BoolVar(&values.jsonOutput, "json", false, "print machine-readable JSON lines")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		return writeSetupFailure(stderr, values.jsonOutput, 2, fmt.Errorf("unexpected argument %q", flags.Arg(0)))
	}
	providersSet := flagWasSet(flags, "providers")
	profileSet := flagWasSet(flags, "audit-profile")
	autoUpdateSet := flagWasSet(flags, "auto-update")
	if !values.nonInteractive {
		return writeSetupFailure(stderr, values.jsonOutput, 2, errors.New("--non-interactive is required"))
	}
	if !providersSet {
		return writeSetupFailure(stderr, values.jsonOutput, 2, errors.New("--providers is required"))
	}
	providers, err := installer.ParseProviders(values.providerNames)
	if err != nil {
		return writeSetupFailure(stderr, values.jsonOutput, 2, err)
	}
	if len(providers) == 0 {
		return writeSetupFailure(stderr, values.jsonOutput, 2, errors.New("at least one provider is required"))
	}
	if !profileSet {
		return writeSetupFailure(stderr, values.jsonOutput, 2, errors.New("--audit-profile is required"))
	}
	if err := validateAuditProfile(values.auditProfile); err != nil || values.auditProfile == "" {
		if err == nil {
			err = errors.New("audit profile must not be empty")
		}
		return writeSetupFailure(stderr, values.jsonOutput, 2, err)
	}
	if !autoUpdateSet {
		return writeSetupFailure(stderr, values.jsonOutput, 2, errors.New("--auto-update is required"))
	}
	if err := validateAutoUpdate(values.autoUpdate); err != nil || values.autoUpdate == "" {
		if err == nil {
			err = errors.New("auto-update mode must not be empty")
		}
		return writeSetupFailure(stderr, values.jsonOutput, 2, err)
	}
	if values.binPath == "" {
		values.binPath, err = dependencies.ResolveExecutable()
		if err != nil {
			return writeSetupFailure(stderr, values.jsonOutput, 2, fmt.Errorf("resolve running executable: %w", err))
		}
	}
	coordinatorDependencies := dependencies.Coordinator
	if values.jsonOutput {
		coordinatorDependencies.Stdout = io.Discard
	} else {
		coordinatorDependencies.Stdout = stdout
	}
	plan, err := dependencies.Prepare(context.Background(), setup.Options{
		BinPath: values.binPath, Providers: providers,
		AuditProfile: config.AuditStorageProfile(values.auditProfile), AutoUpdate: values.autoUpdate,
	}, coordinatorDependencies)
	if err != nil {
		return writeSetupFailure(stderr, values.jsonOutput, 2, fmt.Errorf("preflight: %w", err))
	}
	defer func() { _ = plan.Close() }()
	if err := writeSetupPreview(stdout, values.jsonOutput, plan); err != nil {
		return writeSetupFailure(stderr, values.jsonOutput, 1, err)
	}
	result, err := dependencies.Apply(context.Background(), plan, coordinatorDependencies)
	if err != nil {
		return writeSetupFailure(stderr, values.jsonOutput, 1, err)
	}
	if err := writeSetupComplete(stdout, values.jsonOutput, result); err != nil {
		return writeSetupFailure(stderr, values.jsonOutput, 1, err)
	}
	return 0
}

func defaultSetupCommandDependencies() setupCommandDependencies {
	return setupCommandDependencies{
		ResolveExecutable: os.Executable,
		Prepare:           setup.Prepare,
		Apply:             setup.Apply,
		Coordinator: setup.Dependencies{
			ServiceReady: waitForInstalledDaemon,
		},
	}
}

func flagWasSet(flags *flag.FlagSet, name string) bool {
	set := false
	flags.Visit(func(value *flag.Flag) {
		if value.Name == name {
			set = true
		}
	})
	return set
}

func setupFlagOutput(args []string, stdout io.Writer, stderr io.Writer) io.Writer {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return stdout
		}
	}
	return stderr
}

func writeSetupPreview(writer io.Writer, jsonOutput bool, plan *setup.Plan) error {
	if jsonOutput {
		output := setupPreviewOutput{
			Phase: "preview", Providers: plan.Providers, EffectivePolicy: plan.EffectivePolicy,
			DatabaseExists: plan.Maintenance != nil, ExistingRecords: nil, Maintenance: plan.Maintenance,
		}
		if plan.Maintenance == nil {
			zero := int64(0)
			output.ExistingRecords = &zero
		}
		if err := json.NewEncoder(writer).Encode(output); err != nil {
			wrappedErr := fmt.Errorf("encode setup preview: %w", err)
			slog.Warn("setup preview encoding failed", "err", wrappedErr)
			return wrappedErr
		}
		return nil
	}
	writeAuditStoragePolicy(writer, plan.EffectivePolicy)
	if plan.Maintenance == nil {
		fmt.Fprintln(writer, "existing audit records: 0")
		fmt.Fprintln(writer, "estimated delete bytes: 0")
		return nil
	}
	writeAuditPlan(writer, *plan.Maintenance)
	return nil
}

func writeSetupComplete(writer io.Writer, jsonOutput bool, result setup.Result) error {
	if jsonOutput {
		if err := json.NewEncoder(writer).Encode(setupCompleteOutput{Phase: "complete", Result: result}); err != nil {
			wrappedErr := fmt.Errorf("encode setup result: %w", err)
			slog.Warn("setup result encoding failed", "err", wrappedErr)
			return wrappedErr
		}
		return nil
	}
	fmt.Fprintf(writer, "setup ID: %s\n", result.SetupID)
	for _, probe := range result.Probes {
		fmt.Fprintf(
			writer,
			"verified %s: receipt %d, evaluation %s, decision %s\n",
			probe.Provider,
			probe.ReceiptID,
			probe.EvaluationID,
			probe.Decision,
		)
	}
	return nil
}

func writeSetupFailure(writer io.Writer, jsonOutput bool, exitCode int, err error) int {
	if jsonOutput {
		output := setupErrorOutput{Phase: "error", Error: err.Error(), RepairCommand: ""}
		var applyErr *installer.ApplyError
		if errors.As(err, &applyErr) {
			output.RepairCommand = applyErr.RepairCommand
		}
		if encodeErr := json.NewEncoder(writer).Encode(output); encodeErr != nil {
			fmt.Fprintf(writer, "agent-gate setup: encode error: %v\n", encodeErr)
		}
		return exitCode
	}
	fmt.Fprintf(writer, "agent-gate setup: %v\n", err)
	return exitCode
}
