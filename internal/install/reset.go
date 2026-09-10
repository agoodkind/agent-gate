package installer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

// ResetOptions selects one installation without opening its databases.
type ResetOptions struct {
	ExecutablePath string
	Service        ServiceOptions
	Control        ServiceStatusOptions
	StateDir       string
	CacheDir       string
	RuntimeDir     string
	ConfigDir      string
	Audit          auditstorage.CatalogOptions
	PreservedPaths []string
	Targets        []ResetTarget
	Processes      ProcessControl
}

// ResetTarget names one independently selectable installation artifact.
type ResetTarget string

const (
	// ResetTargetDatabase deletes audit databases and catalog state.
	ResetTargetDatabase ResetTarget = "database"
	// ResetTargetState deletes generated state.
	ResetTargetState ResetTarget = "state"
	// ResetTargetLogs deletes operational logs.
	ResetTargetLogs ResetTarget = "logs"
	// ResetTargetCache deletes cached data.
	ResetTargetCache ResetTarget = "cache"
	// ResetTargetSockets deletes runtime sockets and locks.
	ResetTargetSockets ResetTarget = "sockets"
	// ResetTargetService reinstalls the user service.
	ResetTargetService ResetTarget = "service"
	// ResetTargetBinary reinstalls the current binary.
	ResetTargetBinary ResetTarget = "binary"
	// ResetTargetConfig deletes Agent Gate configuration.
	ResetTargetConfig ResetTarget = "config"
	// ResetTargetHooks removes Agent Gate hook registrations.
	ResetTargetHooks ResetTarget = "hooks"
)

// DefaultResetTargets returns the targets selected by reset --apply.
func DefaultResetTargets() []ResetTarget {
	return []ResetTarget{
		ResetTargetDatabase,
		ResetTargetState,
		ResetTargetLogs,
		ResetTargetCache,
		ResetTargetSockets,
		ResetTargetService,
		ResetTargetBinary,
	}
}

// AllResetTargets returns every supported reset target.
func AllResetTargets() []ResetTarget {
	targets := DefaultResetTargets()
	return append(targets, ResetTargetConfig, ResetTargetHooks)
}

// ResetPlan freezes paths and identities observed before shutdown.
type ResetPlan struct {
	options     ResetOptions
	servicePath string
	catalog     *auditstorage.Catalog
	processes   []ProcessIdentity
}

// ResetError identifies the stage that failed without claiming data rollback.
type ResetError struct {
	Stage string
	Err   error
}

// Error names the failed reset stage.
func (err *ResetError) Error() string { return fmt.Sprintf("reset %s: %v", err.Stage, err.Err) }

// Unwrap exposes the underlying failure.
func (err *ResetError) Unwrap() error { return err.Err }

// PrepareReset freezes the installation, removal scope, and daemon identities.
func PrepareReset(options ResetOptions) (*ResetPlan, error) {
	canonical, err := CanonicalExecutablePath(options.ExecutablePath)
	if err != nil {
		return nil, resetFailure("prepare reset", err)
	}
	options.ExecutablePath = canonical
	options.Service.BinPath = canonical
	service, err := PrepareServiceInstallation(options.Service)
	if err != nil {
		return nil, resetFailure("prepare reset", err)
	}
	options.Control.BinaryPath = canonical
	options.Control = normalizeServiceStatus(options.Control)
	if options.Processes == nil {
		options.Processes = NativeProcessControl{}
	}
	options.PreservedPaths = slices.Clone(options.PreservedPaths)
	if len(options.PreservedPaths) == 0 {
		return nil, errors.New("reset requires preserved configuration and hook paths")
	}
	if err := validateResetTargets(options.Targets); err != nil {
		return nil, err
	}
	for _, path := range []*string{&options.StateDir, &options.CacheDir, &options.RuntimeDir, &options.ConfigDir} {
		if !filepath.IsAbs(*path) || filepath.Base(filepath.Clean(*path)) != "agent-gate" {
			return nil, fmt.Errorf("reset root %q must be an absolute application directory", *path)
		}
		resolved, err := resetCanonicalPath(*path)
		if err != nil {
			return nil, resetFailure("prepare reset", err)
		}
		if filepath.Base(resolved) != "agent-gate" {
			return nil, fmt.Errorf("reset root %q resolves to a shared parent %q", *path, resolved)
		}
		*path = resolved
	}
	for _, path := range slices.Clone(options.PreservedPaths) {
		resolved, err := resetCanonicalPath(path)
		if err != nil {
			return nil, resetFailure("prepare reset", err)
		}
		options.PreservedPaths = append(options.PreservedPaths, resolved)
	}
	plan := &ResetPlan{options: options, servicePath: service.targetPath, catalog: nil, processes: nil}
	catalog, err := auditstorage.NewCatalog(options.Audit)
	if err != nil {
		return nil, resetFailure("prepare reset", err)
	}
	plan.catalog = catalog
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	state, err := InspectService(ctx, options.Control)
	if err != nil {
		return nil, resetFailure("prepare reset", err)
	}
	pids, err := daemonPIDs(ctx, options.Control)
	if err != nil {
		return nil, resetFailure("prepare reset", err)
	}
	if state.PID > 1 {
		pids = append(pids, state.PID)
	}
	slices.Sort(pids)
	for _, pid := range slices.Compact(pids) {
		identity, err := options.Processes.Inspect(pid)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, resetFailure("prepare reset", err)
		}
		if identity.Executable != canonical || identity.PID != pid || identity.Start == "" {
			return nil, errors.New("daemon identity belongs to a different installation")
		}
		plan.processes = append(plan.processes, identity)
	}
	return plan, nil
}

func validateResetTargets(targets []ResetTarget) error {
	if len(targets) == 0 {
		return errors.New("reset requires a target")
	}
	for _, target := range targets {
		if !slices.Contains(AllResetTargets(), target) {
			return fmt.Errorf("unknown reset target %q", target)
		}
	}
	return nil
}

func (plan *ResetPlan) removalPaths() []string {
	options := plan.options
	var paths []string
	if plan.selects(ResetTargetState) {
		paths = append(paths, options.StateDir)
	} else if plan.selects(ResetTargetLogs) {
		for _, name := range []string{
			"agent-gate.log",
			"agent-gate.jsonl",
			"agent-gate.jsonl.lock",
			"fail-open.jsonl",
		} {
			paths = append(paths, filepath.Join(options.StateDir, name))
		}
	}
	if plan.selects(ResetTargetCache) {
		paths = append(paths, options.CacheDir)
	}
	if plan.selects(ResetTargetSockets) {
		paths = append(paths, options.RuntimeDir)
	}
	if plan.selects(ResetTargetService) {
		paths = append(paths, plan.servicePath)
	}
	if plan.selects(ResetTargetBinary) {
		paths = append(paths, options.ExecutablePath)
	}
	if plan.selects(ResetTargetConfig) {
		paths = append(paths, options.ConfigDir)
	}
	return paths
}

func (plan *ResetPlan) selects(target ResetTarget) bool {
	return slices.Contains(plan.options.Targets, target)
}

// ApplyReset stages the executable, proves shutdown, purges storage, and restores it.
// The caller holds the operation lock through the subsequent existing installer call.
func ApplyReset(ctx context.Context, plan *ResetPlan) (resultErr error) {
	slog.DebugContext(ctx, "apply installation reset")
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "installation reset failed", "err", resultErr)
		}
	}()
	if plan == nil {
		return &ResetError{Stage: "prepare", Err: errors.New("reset plan is required")}
	}
	paths := plan.removalPaths()
	if err := plan.validatePaths(paths); err != nil {
		return &ResetError{Stage: "preflight", Err: err}
	}
	stage, err := prepareResetStage(ctx, plan, paths)
	if err != nil {
		return &ResetError{Stage: "stage", Err: err}
	}
	teardown := false
	if stage.ready {
		defer func() {
			resultErr = errors.Join(resultErr, plan.cleanupStage(context.WithoutCancel(ctx), stage.dir, stage.path, teardown))
		}()
	}
	if err := StopService(ctx, plan.options.Control); err != nil {
		return &ResetError{Stage: "stop service", Err: err}
	}
	for _, identity := range plan.processes {
		if err := stopResetProcess(ctx, plan.options.Processes, identity, 10*time.Second); err != nil {
			return &ResetError{Stage: "stop daemon", Err: err}
		}
	}
	remaining, err := daemonPIDs(ctx, plan.options.Control)
	if err != nil {
		return &ResetError{Stage: "stop daemon", Err: err}
	}
	for _, pid := range remaining {
		if _, err := plan.options.Processes.Inspect(pid); !errors.Is(err, os.ErrNotExist) {
			return &ResetError{Stage: "stop daemon", Err: errors.Join(err, errors.New("a daemon appeared after shutdown"))}
		}
	}
	if plan.selects(ResetTargetDatabase) || plan.selects(ResetTargetState) {
		if err := plan.catalog.Purge(ctx, plan.validatePaths); err != nil {
			return &ResetError{Stage: "purge", Err: err}
		}
	}
	teardown = stage.ready
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return &ResetError{Stage: "remove", Err: err}
		}
		if err := os.RemoveAll(path); err != nil {
			return &ResetError{Stage: "remove", Err: fmt.Errorf("%s: %w", path, err)}
		}
	}
	if stage.ready {
		if err := copyResetExecutable(ctx, stage.path, plan.options.ExecutablePath); err != nil {
			return &ResetError{Stage: "restore", Err: err}
		}
		teardown = false
	}
	return nil
}

type resetStage struct {
	dir   string
	path  string
	ready bool
}

func prepareResetStage(ctx context.Context, plan *ResetPlan, paths []string) (resetStage, error) {
	slog.DebugContext(ctx, "stage reset executable", "reset_binary", plan.selects(ResetTargetBinary))
	if !plan.selects(ResetTargetBinary) {
		return resetStage{dir: "", path: "", ready: false}, nil
	}
	dir, err := os.MkdirTemp(filepath.Dir(plan.options.RuntimeDir), ".agent-gate-reset-")
	if err != nil {
		slog.WarnContext(ctx, "create reset stage failed", "err", err)
		return resetStage{dir: "", path: "", ready: false}, fmt.Errorf("create reset stage: %w", err)
	}
	stage := resetStage{dir: dir, path: filepath.Join(dir, "agent-gate"), ready: false}
	for _, path := range paths {
		overlap, err := resetPathContains(path, stage.dir)
		if err != nil || overlap {
			_ = os.RemoveAll(stage.dir)
			slog.WarnContext(ctx, "reset stage overlaps removal scope", "path", path, "err", err)
			return resetStage{}, errors.Join(err, errors.New("staging overlaps removal scope"))
		}
	}
	if err := copyResetExecutable(ctx, plan.options.ExecutablePath, stage.path); err != nil {
		_ = os.RemoveAll(stage.dir)
		slog.WarnContext(ctx, "copy reset executable failed", "err", err)
		return resetStage{}, err
	}
	stage.ready = true
	return stage, nil
}

func (plan *ResetPlan) cleanupStage(ctx context.Context, directory string, executable string, restore bool) error {
	slog.DebugContext(ctx, "clean reset executable staging")
	if restore {
		if err := copyResetExecutable(ctx, executable, plan.options.ExecutablePath); err != nil {
			return &ResetError{Stage: "restore", Err: resetFailure("executable retained at "+executable, err)}
		}
	}
	if err := os.RemoveAll(directory); err != nil {
		return &ResetError{Stage: "stage cleanup", Err: err}
	}
	return nil
}

func resetFailure(action string, err error) error {
	wrapped := fmt.Errorf("%s: %w", action, err)
	slog.Warn(action, "err", wrapped)
	return wrapped
}
