package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

// Options selects one automated setup plan.
type Options struct {
	BinPath      string
	Providers    []installer.Provider
	AuditProfile config.AuditStorageProfile
	AutoUpdate   string
}

// Plan retains a complete installation and its read-only maintenance preview.
type Plan struct {
	Installation    *installer.InstallationPlan
	Providers       []installer.Provider
	EffectivePolicy config.AuditStoragePolicy
	Maintenance     *auditmaintenance.Plan
	binPath         string
	homeDir         string
	installation    *installer.InstallationPlan
	probeConfig     *config.Config
	providers       []installer.Provider
	prepared        bool
	setupID         string
}

// Result records the durable proof produced by setup.
type Result struct {
	SetupID string        `json:"setup_id"`
	Probes  []ProbeResult `json:"probes"`
}

// Dependencies supplies setup boundaries that tests can replace.
type Dependencies struct {
	PrepareInstallation  func(installer.InstallationOptions) (*installer.InstallationPlan, error)
	ApplyInstallation    func(*installer.InstallationPlan) (installer.ApplyResult, error)
	VerifyInstalledHooks func(context.Context, ProbeRequest) ([]ProbeResult, error)
	Preview              func(context.Context, string, config.AuditStoragePolicy, time.Time) (auditmaintenance.Plan, error)
	Stat                 func(string) (os.FileInfo, error)
	Lstat                func(string) (os.FileInfo, error)
	Now                  func() time.Time
	NewSetupID           func() (string, error)
	ServiceReady         func(string) error
	HomeDir              string
	HookTemplatesDir     string
	ServiceTemplatesDir  string
	Stdout               io.Writer
}

// Prepare validates every setup layer and previews existing audit data without writing.
func Prepare(ctx context.Context, options Options, dependencies Dependencies) (_ *Plan, resultErr error) {
	if len(options.Providers) == 0 {
		return nil, errors.New("at least one provider is required")
	}
	canonicalBinPath, err := installer.CanonicalExecutablePath(options.BinPath)
	if err != nil {
		return nil, fmt.Errorf("resolve executable: %w", err)
	}
	dependencies = setupDependenciesWithDefaults(dependencies)
	setupID, err := dependencies.NewSetupID()
	if err != nil {
		wrappedErr := fmt.Errorf("create setup ID: %w", err)
		slog.WarnContext(ctx, "setup ID creation failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	if setupID == "" {
		return nil, errors.New("created setup ID is empty")
	}
	hooks := installer.DefaultHooksOptions(canonicalBinPath)
	hooks.HomeDir = dependencies.HomeDir
	hooks.TemplatesDir = dependencies.HookTemplatesDir
	hooks.Stdout = dependencies.Stdout
	hooks.Providers = append([]installer.Provider(nil), options.Providers...)
	service := installer.ServiceOptions{
		BinPath: canonicalBinPath, ServiceTemplatesDir: dependencies.ServiceTemplatesDir,
		HomeDir: dependencies.HomeDir, ConfigHome: "", StateHome: "",
		Stdout: dependencies.Stdout, Runner: nil, Ready: nil,
	}
	if dependencies.ServiceReady != nil {
		service.Ready = func() error { return dependencies.ServiceReady(canonicalBinPath) }
	}
	installation, err := dependencies.PrepareInstallation(installer.InstallationOptions{
		Config: &config.EnsureDefaultsOptions{
			AutoUpdateMode: options.AutoUpdate,
			AuditProfile:   options.AuditProfile,
		},
		Service: &service,
		Hooks:   &hooks,
	})
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			if closeErr := installation.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("close installation plan: %w", closeErr))
			}
		}
	}()
	if installation.Config == nil || installation.Config.Config == nil {
		return nil, errors.New("prepared installation is missing configuration")
	}
	preparedConfig := installation.Config.Config
	policy := preparedConfig.AuditStoragePolicy()
	databasePath := preparedConfig.AuditSQLitePath()
	probeConfig := *preparedConfig
	probeConfig.Audit.Outputs.SQLite.Path = databasePath
	plan := &Plan{
		Installation:    installation,
		Providers:       append([]installer.Provider(nil), options.Providers...),
		EffectivePolicy: policy,
		Maintenance:     nil,
		binPath:         canonicalBinPath,
		homeDir:         dependencies.HomeDir,
		installation:    installation,
		probeConfig:     &probeConfig,
		providers:       append([]installer.Provider(nil), options.Providers...),
		prepared:        true,
		setupID:         setupID,
	}
	if _, err := dependencies.Stat(databasePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if _, lstatErr := dependencies.Lstat(databasePath); lstatErr == nil {
				wrappedErr := fmt.Errorf(
					"audit database path resolves to a missing target: %s: %w",
					databasePath,
					os.ErrNotExist,
				)
				slog.WarnContext(ctx, "setup audit database path rejected", "err", wrappedErr)
				return nil, wrappedErr
			} else if !errors.Is(lstatErr, os.ErrNotExist) {
				wrappedErr := fmt.Errorf("inspect audit database path entry: %w", lstatErr)
				slog.WarnContext(ctx, "setup audit database path inspection failed", "err", wrappedErr)
				return nil, wrappedErr
			}
			return plan, nil
		}
		wrappedErr := fmt.Errorf("inspect audit database: %w", err)
		slog.WarnContext(ctx, "setup audit database inspection failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	maintenance, err := dependencies.Preview(ctx, databasePath, policy, dependencies.Now().UTC())
	if err != nil {
		wrappedErr := fmt.Errorf("preview audit maintenance: %w", err)
		slog.WarnContext(ctx, "setup audit maintenance preview failed", "err", wrappedErr)
		return nil, wrappedErr
	}
	plan.Maintenance = &maintenance
	return plan, nil
}

// Apply installs the retained plan and verifies each installed provider through durable stores.
func Apply(ctx context.Context, plan *Plan, dependencies Dependencies) (Result, error) {
	installation, providers := setupPlanComponents(plan)
	if installation == nil {
		return Result{}, errors.New("setup plan is required")
	}
	dependencies = setupDependenciesWithDefaults(dependencies)
	if _, err := dependencies.ApplyInstallation(installation); err != nil {
		return Result{}, err
	}
	if plan.setupID == "" {
		return Result{}, errors.New("setup plan is missing a verification ID")
	}
	probes, err := dependencies.VerifyInstalledHooks(ctx, ProbeRequest{
		SetupID: plan.setupID, Providers: providers,
		HomeDir: plan.homeDir, BinPath: plan.binPath, Config: plan.probeConfig,
		Timeout: 0,
	})
	if err != nil {
		return Result{}, err
	}
	return Result{SetupID: plan.setupID, Probes: probes}, nil
}

// Close releases a prepared setup plan without applying it.
func (plan *Plan) Close() error {
	installation, _ := setupPlanComponents(plan)
	if installation == nil {
		return nil
	}
	if err := installation.Close(); err != nil {
		wrappedErr := fmt.Errorf("close installation plan: %w", err)
		slog.Warn("setup plan close failed", "err", wrappedErr)
		return wrappedErr
	}
	return nil
}

func setupDependenciesWithDefaults(dependencies Dependencies) Dependencies {
	if dependencies.PrepareInstallation == nil {
		dependencies.PrepareInstallation = installer.PrepareInstallation
	}
	if dependencies.ApplyInstallation == nil {
		dependencies.ApplyInstallation = installer.ApplyInstallation
	}
	if dependencies.VerifyInstalledHooks == nil {
		dependencies.VerifyInstalledHooks = VerifyInstalledHooks
	}
	if dependencies.Preview == nil {
		dependencies.Preview = auditmaintenance.Preview
	}
	if dependencies.Stat == nil {
		dependencies.Stat = os.Stat
	}
	if dependencies.Lstat == nil {
		dependencies.Lstat = os.Lstat
	}
	if dependencies.Now == nil {
		dependencies.Now = time.Now
	}
	if dependencies.NewSetupID == nil {
		dependencies.NewSetupID = newSetupID
	}
	if dependencies.Stdout == nil {
		dependencies.Stdout = io.Discard
	}
	return dependencies
}

func setupPlanComponents(plan *Plan) (*installer.InstallationPlan, []installer.Provider) {
	if plan == nil {
		return nil, nil
	}
	if plan.prepared {
		return plan.installation, append([]installer.Provider(nil), plan.providers...)
	}
	return plan.Installation, append([]installer.Provider(nil), plan.Providers...)
}

func newSetupID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		wrappedErr := fmt.Errorf("read random setup ID: %w", err)
		slog.Warn("setup ID random source failed", "err", wrappedErr)
		return "", wrappedErr
	}
	return "setup-" + hex.EncodeToString(value[:]), nil
}
