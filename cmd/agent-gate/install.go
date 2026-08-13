package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/hook"
	agentinstall "goodkind.io/agent-gate/internal/install"
)

const (
	installSubcommandAll     = "all"
	installSubcommandHooks   = "hooks"
	installSubcommandService = "service"
	installReadinessTimeout  = 10 * time.Second
	installReadinessInterval = 200 * time.Millisecond
)

type installFlagValues struct {
	binPath             string
	templatesDir        string
	serviceTemplatesDir string
	autoUpdate          string
	auditProfile        string
	noConfig            bool
	noService           bool
	noClaude            bool
	noCodex             bool
	noCursor            bool
	noGemini            bool
	noCopilot           bool
}

type daemonIdentity struct {
	ExecutablePath string
	BuildHash      string
}

type daemonStatusLookup func(context.Context) (daemonIdentity, error)

type installDependencies struct {
	resolveExecutable   func() (string, error)
	validateExecutable  func(string) error
	prepareInstallation func(agentinstall.InstallationOptions) (*agentinstall.InstallationPlan, error)
	applyInstallation   func(*agentinstall.InstallationPlan) (agentinstall.ApplyResult, error)
	prepareHooks        func(agentinstall.HooksOptions) (*agentinstall.HookInstallationPlan, error)
	applyHooks          func(*agentinstall.HookInstallationPlan) error
	validateService     func(agentinstall.ServiceOptions) error
	ensureConfig        func(config.EnsureDefaultsOptions) error
	validateConfig      func() error
	installService      func(agentinstall.ServiceOptions) error
	waitForReady        func(string) error
}

func runInstall(args []string) int {
	return runInstallWithDependencies(args, defaultInstallDependencies())
}

func runInstallWithDependencies(args []string, dependencies installDependencies) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-gate install {hooks|service|all} [--bin-path PATH]")
		return 2
	}
	switch args[0] {
	case installSubcommandHooks:
		return runInstallHooks(args[1:], dependencies)
	case installSubcommandService:
		return runInstallService(args[1:], dependencies)
	case installSubcommandAll:
		return runInstallAll(args[1:], dependencies)
	default:
		fmt.Fprintf(os.Stderr, "agent-gate install: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runInstallHooks(args []string, dependencies installDependencies) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install hooks", &values)
	registerHookInstallFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if err := resolveAndValidateInstallValues(&values, dependencies); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: preflight: %v\n", err)
		return 2
	}
	if dependencies.prepareInstallation != nil && dependencies.applyInstallation != nil {
		hooks := hookOptions(values)
		return runPreparedInstallation(
			"agent-gate install hooks",
			agentinstall.InstallationOptions{Hooks: &hooks},
			dependencies,
		)
	}
	plan, err := dependencies.prepareHooks(hookOptions(values))
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: preflight: hooks: %v\n", err)
		return 2
	}
	if err := dependencies.applyHooks(plan); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install hooks: hooks: %v\n", err)
		return 1
	}
	return 0
}

func runInstallService(args []string, dependencies installDependencies) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install service", &values)
	registerServiceInstallFlags(flags, &values)
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install service: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if err := resolveAndValidateInstallValues(&values, dependencies); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install service: preflight: %v\n", err)
		return 2
	}
	if dependencies.prepareInstallation != nil && dependencies.applyInstallation != nil {
		service := serviceOptions(values, dependencies)
		return runPreparedInstallation(
			"agent-gate install service",
			agentinstall.InstallationOptions{Service: &service},
			dependencies,
		)
	}
	if err := dependencies.validateService(serviceOptions(values, dependencies)); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install service: preflight: service: %v\n", err)
		return 2
	}
	if err := dependencies.installService(serviceOptions(values, dependencies)); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install service: service: %v\n", err)
		return 1
	}
	return 0
}

func runInstallAll(args []string, dependencies installDependencies) int {
	values := installFlagValues{}
	flags := newInstallFlagSet("agent-gate install all", &values)
	registerHookInstallFlags(flags, &values)
	registerServiceInstallFlags(flags, &values)
	flags.BoolVar(&values.noConfig, "no-config", false, "skip default config creation and merge")
	flags.BoolVar(&values.noService, "no-service", false, "skip service installation and readiness")
	flags.StringVar(&values.autoUpdate, "auto-update", "", "set update mode: check, apply, or off")
	flags.StringVar(&values.auditProfile, "audit-profile", "", "set audit storage profile: balanced, full, or minimal")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "agent-gate install all: unexpected argument %q\n", flags.Arg(0))
		return 2
	}
	if err := validateAutoUpdate(values.autoUpdate); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: %v\n", err)
		return 2
	}
	if err := validateAuditProfile(values.auditProfile); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: %v\n", err)
		return 2
	}
	if err := resolveAndValidateInstallValues(&values, dependencies); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: %v\n", err)
		return 2
	}
	if dependencies.prepareInstallation != nil && dependencies.applyInstallation != nil {
		installationOptions := agentinstall.InstallationOptions{}
		if !values.noConfig {
			options := configOptions(values)
			installationOptions.Config = &options
		}
		if !values.noService {
			service := serviceOptions(values, dependencies)
			installationOptions.Service = &service
		}
		hooks := hookOptions(values)
		installationOptions.Hooks = &hooks
		if values.noConfig {
			if err := dependencies.validateConfig(); err != nil {
				fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: config: %v\n", err)
				return 2
			}
		}
		return runPreparedInstallation(
			"agent-gate install all",
			installationOptions,
			dependencies,
		)
	}
	hookPlan, err := dependencies.prepareHooks(hookOptions(values))
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: hooks: %v\n", err)
		return 2
	}
	if !values.noService {
		if err := dependencies.validateService(serviceOptions(values, dependencies)); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate install all: preflight: service: %v\n", err)
			return 2
		}
	}
	if !values.noConfig {
		if err := dependencies.ensureConfig(configOptions(values)); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate install all: config: %v\n", err)
			return 1
		}
	}
	if err := dependencies.validateConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: config validation: %v\n", err)
		return 1
	}
	if !values.noService {
		if err := dependencies.installService(serviceOptions(values, dependencies)); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate install all: service: %v\n", err)
			return 1
		}
	}
	if err := dependencies.applyHooks(hookPlan); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate install all: hooks: %v\n", err)
		return 1
	}
	return 0
}

func defaultInstallDependencies() installDependencies {
	return installDependencies{
		resolveExecutable:   os.Executable,
		validateExecutable:  agentinstall.ValidateExecutable,
		prepareInstallation: agentinstall.PrepareInstallation,
		applyInstallation:   agentinstall.ApplyInstallation,
		prepareHooks:        agentinstall.PrepareHookInstallation,
		applyHooks:          agentinstall.ApplyHookInstallation,
		validateService:     agentinstall.ValidateServiceOptions,
		ensureConfig: func(options config.EnsureDefaultsOptions) error {
			_, err := config.EnsureDefaults(options)
			return err
		},
		validateConfig: func() error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if validationErrors := hook.ValidateConfig(cfg); len(validationErrors) > 0 {
				return validationErrors[0]
			}
			return nil
		},
		installService: agentinstall.InstallService,
		waitForReady:   waitForInstalledDaemon,
	}
}

func runPreparedInstallation(
	commandName string,
	options agentinstall.InstallationOptions,
	dependencies installDependencies,
) int {
	plan, err := dependencies.prepareInstallation(options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: preflight: %v\n", commandName, err)
		return 2
	}
	defer func() { _ = plan.Close() }()
	if _, err := dependencies.applyInstallation(plan); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", commandName, err)
		return 1
	}
	return 0
}

func resolveAndValidateInstallValues(
	values *installFlagValues,
	dependencies installDependencies,
) error {
	if values.binPath == "" {
		binPath, err := dependencies.resolveExecutable()
		if err != nil {
			wrappedErr := fmt.Errorf("resolve running executable: %w", err)
			slog.Warn("install executable resolution failed", "err", wrappedErr)
			return wrappedErr
		}
		values.binPath = binPath
	}
	canonicalPath, err := agentinstall.CanonicalExecutablePath(values.binPath)
	if err != nil {
		return err
	}
	values.binPath = canonicalPath
	if err := dependencies.validateExecutable(values.binPath); err != nil {
		return err
	}
	return nil
}

func validateAutoUpdate(value string) error {
	switch value {
	case "", config.UpdateModeCheck, config.UpdateModeApply, "off":
		return nil
	default:
		return fmt.Errorf("auto-update mode must be %q, %q, or %q", config.UpdateModeCheck, config.UpdateModeApply, "off")
	}
}

func validateAuditProfile(value string) error {
	switch config.AuditStorageProfile(value) {
	case "", config.AuditStorageProfileBalanced, config.AuditStorageProfileFull,
		config.AuditStorageProfileMinimal:
		return nil
	default:
		return fmt.Errorf(
			"audit profile must be %q, %q, or %q",
			config.AuditStorageProfileBalanced,
			config.AuditStorageProfileFull,
			config.AuditStorageProfileMinimal,
		)
	}
}

func configOptions(values installFlagValues) config.EnsureDefaultsOptions {
	return config.EnsureDefaultsOptions{
		AutoUpdateMode: values.autoUpdate,
		AuditProfile:   config.AuditStorageProfile(values.auditProfile),
	}
}

func newInstallFlagSet(name string, values *installFlagValues) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&values.binPath, "bin-path", "", "path to the installed agent-gate binary")
	return flags
}

func registerHookInstallFlags(flags *flag.FlagSet, values *installFlagValues) {
	flags.StringVar(&values.templatesDir, "templates", "", "local hook template directory")
	flags.BoolVar(&values.noClaude, "no-claude", false, "skip Claude hook config update")
	flags.BoolVar(&values.noCodex, "no-codex", false, "skip Codex hook config update")
	flags.BoolVar(&values.noCursor, "no-cursor", false, "skip Cursor hook config update")
	flags.BoolVar(&values.noGemini, "no-gemini", false, "skip Gemini hook config update")
	flags.BoolVar(&values.noCopilot, "no-copilot", false, "skip GitHub Copilot Chat hook config update")
}

func registerServiceInstallFlags(flags *flag.FlagSet, values *installFlagValues) {
	flags.StringVar(&values.serviceTemplatesDir, "service-templates", "", "local service template directory")
}

func hookOptions(values installFlagValues) agentinstall.HooksOptions {
	options := agentinstall.DefaultHooksOptions(values.binPath)
	options.TemplatesDir = values.templatesDir
	options.Stdout = os.Stdout
	options.InstallClaude = !values.noClaude
	options.InstallCodex = !values.noCodex
	options.InstallCursor = !values.noCursor
	options.InstallGemini = !values.noGemini
	options.InstallCopilot = !values.noCopilot
	return options
}

func serviceOptions(
	values installFlagValues,
	dependencies installDependencies,
) agentinstall.ServiceOptions {
	return agentinstall.ServiceOptions{
		BinPath:             values.binPath,
		ServiceTemplatesDir: values.serviceTemplatesDir,
		Stdout:              os.Stdout,
		Ready: func() error {
			return dependencies.waitForReady(values.binPath)
		},
	}
}

func waitForInstalledDaemon(binPath string) error {
	canonicalPath, err := agentinstall.CanonicalExecutablePath(binPath)
	if err != nil {
		return err
	}
	buildHash, err := executableBuildHash(canonicalPath)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), installReadinessTimeout)
	defer cancel()
	return waitForDaemonReady(
		ctx,
		canonicalPath,
		buildHash,
		installReadinessInterval,
		lookupDaemonIdentity,
	)
}

func waitForDaemonReady(
	ctx context.Context,
	expectedPath string,
	expectedBuildHash string,
	pollInterval time.Duration,
	status daemonStatusLookup,
) error {
	var lastErr error
	for {
		identity, err := status(ctx)
		reportedPath := identity.ExecutablePath
		if err == nil {
			canonicalPath, resolveErr := agentinstall.CanonicalExecutablePath(identity.ExecutablePath)
			if resolveErr != nil {
				err = fmt.Errorf("resolve daemon executable identity: %w", resolveErr)
			} else {
				reportedPath = canonicalPath
				if reportedPath == expectedPath && identity.BuildHash == expectedBuildHash {
					return nil
				}
			}
		}
		attemptErr := err
		if attemptErr == nil {
			attemptErr = fmt.Errorf(
				"daemon identity mismatch: executable=%q buildHash=%q, want executable=%q buildHash=%q",
				reportedPath,
				identity.BuildHash,
				expectedPath,
				expectedBuildHash,
			)
		}
		if ctx.Err() != nil {
			if lastErr == nil || !isContextTermination(attemptErr) {
				lastErr = attemptErr
			}
			return daemonReadinessTimeout(ctx, lastErr)
		}
		lastErr = attemptErr

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return daemonReadinessTimeout(ctx, lastErr)
		case <-timer.C:
		}
	}
}

func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func daemonReadinessTimeout(ctx context.Context, lastErr error) error {
	if lastErr == nil {
		lastErr = ctx.Err()
	}
	wrappedErr := fmt.Errorf("daemon readiness timed out: %w", lastErr)
	slog.WarnContext(ctx, "install daemon readiness timed out", "err", wrappedErr)
	return wrappedErr
}

func lookupDaemonIdentity(ctx context.Context) (daemonIdentity, error) {
	client, err := daemon.Connect(ctx)
	if err != nil {
		return daemonIdentity{}, err
	}
	defer func() { _ = client.Close() }()
	status, err := client.StatusContext(ctx)
	if err != nil {
		return daemonIdentity{}, err
	}
	return daemonIdentity{
		ExecutablePath: status.GetExecutablePath(),
		BuildHash:      status.GetBuildHash(),
	}, nil
}

func executableBuildHash(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		wrappedErr := fmt.Errorf("open installer executable for hashing: %w", err)
		slog.Warn("install executable hash open failed", "path", path, "err", wrappedErr)
		return "", wrappedErr
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		wrappedErr := fmt.Errorf("hash installer executable: %w", err)
		slog.Warn("install executable hash failed", "path", path, "err", wrappedErr)
		return "", wrappedErr
	}
	return hex.EncodeToString(hash.Sum(nil))[:12], nil
}
