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
	Processes      ProcessControl
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

func (plan *ResetPlan) removalPaths() ([]string, error) {
	options := plan.options
	paths := []string{options.StateDir, options.CacheDir, options.RuntimeDir, plan.servicePath, options.ExecutablePath}
	entries, err := os.ReadDir(options.ConfigDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, resetFailure("prepare reset", err)
	}
	for _, entry := range entries {
		path := filepath.Join(options.ConfigDir, entry.Name())
		preserved := false
		for _, keep := range options.PreservedPaths {
			if filepath.Clean(path) == filepath.Clean(keep) {
				preserved = true
				break
			}
		}
		if !preserved {
			paths = append(paths, path)
		}
	}
	return paths, nil
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
	stageDir, err := os.MkdirTemp(filepath.Dir(plan.options.RuntimeDir), ".agent-gate-reset-")
	if err != nil {
		return &ResetError{Stage: "stage", Err: err}
	}
	staged := filepath.Join(stageDir, "agent-gate")
	stageReady := false
	teardown := false
	defer func() {
		resultErr = errors.Join(resultErr, plan.cleanupStage(context.WithoutCancel(ctx), stageDir, staged, teardown && stageReady))
	}()
	paths, err := plan.removalPaths()
	if err != nil {
		return &ResetError{Stage: "preflight", Err: err}
	}
	if err := plan.validatePaths(paths); err != nil {
		return &ResetError{Stage: "preflight", Err: err}
	}
	for _, path := range paths {
		overlap, err := resetPathContains(path, stageDir)
		if err != nil || overlap {
			return &ResetError{Stage: "stage", Err: errors.Join(err, errors.New("staging overlaps removal scope"))}
		}
	}
	if err := copyResetExecutable(ctx, plan.options.ExecutablePath, staged); err != nil {
		return &ResetError{Stage: "stage", Err: err}
	}
	stageReady = true
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
	if err := plan.catalog.Purge(ctx, plan.validatePaths); err != nil {
		return &ResetError{Stage: "purge", Err: err}
	}
	teardown = true
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return &ResetError{Stage: "remove", Err: err}
		}
		if err := os.RemoveAll(path); err != nil {
			return &ResetError{Stage: "remove", Err: fmt.Errorf("%s: %w", path, err)}
		}
	}
	if err := copyResetExecutable(ctx, staged, plan.options.ExecutablePath); err != nil {
		return &ResetError{Stage: "restore", Err: err}
	}
	teardown = false
	return nil
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
