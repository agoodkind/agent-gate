package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/gofrs/flock"
	"github.com/pelletier/go-toml/v2"

	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
)

type resetDependencies struct {
	resolveExecutable func() (string, error)
	prepareReset      func(installer.ResetOptions) (*installer.ResetPlan, error)
	applyReset        func(context.Context, *installer.ResetPlan) error
	install           installDependencies
	prepareHooks      func(installer.HooksOptions) (*installer.HookInstallationPlan, error)
	applyHooks        func(*installer.HookInstallationPlan) error
}

func defaultResetDependencies() resetDependencies {
	return resetDependencies{resolveExecutable: os.Executable, prepareReset: installer.PrepareReset, applyReset: installer.ApplyReset, install: defaultInstallDependencies(), prepareHooks: installer.PrepareHookRemoval, applyHooks: installer.ApplyHookInstallation}
}

func runReset(args []string, stdout io.Writer, stderr io.Writer) int {
	return runResetWithDependencies(args, stdout, stderr, defaultResetDependencies())
}

func runResetWithDependencies(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	dependencies resetDependencies,
) int {
	targets, apply, helpTarget, err := parseResetCommand(args)
	if err != nil {
		fmt.Fprintf(stderr, "agent-gate reset: %v\n", err)
		writeResetHelp(stderr, "")
		return 2
	}
	if !apply {
		writeResetHelp(stdout, helpTarget)
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lockPath := filepath.Join(filepath.Dir(config.RuntimeDir()), "agent-gate-reset.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return reportResetError(stderr, "lock", err)
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		return reportResetError(stderr, "lock", errors.Join(err, ctx.Err()))
	}
	defer func() { _ = lock.Close() }()
	if isOnlyResetTarget(targets, installer.ResetTargetHooks) {
		return applyHookReset(stdout, stderr, dependencies)
	}
	path, err := dependencies.resolveExecutable()
	if err != nil {
		return reportResetError(stderr, "prepare", err)
	}
	path, err = installer.CanonicalExecutablePath(path)
	if err != nil {
		return reportResetError(stderr, "prepare", err)
	}
	options, err := resetOptions(path, stdout, targets)
	if err != nil {
		return reportResetError(stderr, "prepare", err)
	}
	plan, err := dependencies.prepareReset(options)
	if err != nil {
		return reportResetError(stderr, "prepare", err)
	}
	if err := dependencies.applyReset(ctx, plan); err != nil {
		return reportResetError(stderr, "apply", err)
	}
	if slices.Contains(targets, installer.ResetTargetHooks) {
		return applyHookReset(stdout, stderr, dependencies)
	}
	if !resetNeedsServiceReinstall(targets) {
		return 0
	}
	code := runInstallWithDependencies([]string{"service", "--bin-path", path}, dependencies.install)
	if code != 0 {
		fmt.Fprintln(stderr, "agent-gate reset: reinstall failed; selected data removed")
	}
	return code
}

func reportResetError(stderr io.Writer, stage string, err error) int {
	fmt.Fprintf(stderr, "agent-gate reset: %s: %v\n", stage, err)
	return 1
}

func resetOptions(
	path string,
	stdout io.Writer,
	targets []installer.ResetTarget,
) (installer.ResetOptions, error) {
	var cfg config.Config
	if slices.Contains(targets, installer.ResetTargetDatabase) {
		data, err := os.ReadFile(config.Path())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return installer.ResetOptions{}, err
		}
		if len(data) > 0 {
			if err := toml.Unmarshal(data, &cfg); err != nil {
				slog.Warn("reset configuration parse failed", "err", err)
				return installer.ResetOptions{}, fmt.Errorf("parse removal paths: %w", err)
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return installer.ResetOptions{}, err
	}
	return installer.ResetOptions{
		ExecutablePath: path, Service: installer.ServiceOptions{BinPath: path, Stdout: stdout},
		StateDir: config.DefaultStateDir(), CacheDir: config.DefaultCacheDir(), RuntimeDir: config.RuntimeDir(), ConfigDir: config.DefaultConfigDir(), Audit: cfg.AuditCatalogOptions(), Targets: targets,
		PreservedPaths: resetPreservedPaths(home, targets),
	}, nil
}

func resetPreservedPaths(home string, targets []installer.ResetTarget) []string {
	paths := []string{filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".cursor", "hooks.json"), filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".copilot", "hooks", "agent-gate.json")}
	if !slices.Contains(targets, installer.ResetTargetConfig) {
		paths = append(paths, config.DefaultConfigDir())
	}
	return paths
}

func applyHookReset(stdout io.Writer, stderr io.Writer, dependencies resetDependencies) int {
	home, err := os.UserHomeDir()
	if err != nil {
		return reportResetError(stderr, "remove hooks", err)
	}
	hooks, err := dependencies.prepareHooks(installer.HooksOptions{HomeDir: home, Stdout: stdout})
	if err != nil {
		return reportResetError(stderr, "remove hooks", err)
	}
	if err := dependencies.applyHooks(hooks); err != nil {
		return reportResetError(stderr, "remove hooks", err)
	}
	return 0
}

func isOnlyResetTarget(targets []installer.ResetTarget, target installer.ResetTarget) bool {
	return len(targets) == 1 && targets[0] == target
}

func resetNeedsServiceReinstall(targets []installer.ResetTarget) bool {
	return !isOnlyResetTarget(targets, installer.ResetTargetConfig) &&
		!isOnlyResetTarget(targets, installer.ResetTargetHooks)
}

var resetTargets = map[string]installer.ResetTarget{
	"database": installer.ResetTargetDatabase,
	"state":    installer.ResetTargetState,
	"logs":     installer.ResetTargetLogs,
	"cache":    installer.ResetTargetCache,
	"sockets":  installer.ResetTargetSockets,
	"service":  installer.ResetTargetService,
	"binary":   installer.ResetTargetBinary,
	"config":   installer.ResetTargetConfig,
	"hooks":    installer.ResetTargetHooks,
}

func parseResetCommand(args []string) ([]installer.ResetTarget, bool, string, error) {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "-h")) {
		return nil, false, "", nil
	}
	if len(args) == 1 && args[0] == "--apply" {
		return installer.DefaultResetTargets(), true, "", nil
	}
	target, found := resetTargets[args[0]]
	if !found {
		return nil, false, "", fmt.Errorf("unknown target %q", args[0])
	}
	if len(args) == 1 || (len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		return []installer.ResetTarget{target}, false, args[0], nil
	}
	if len(args) == 2 && args[1] == "--apply" {
		return []installer.ResetTarget{target}, true, args[0], nil
	}
	return nil, false, "", errors.New("expected a target followed by --apply")
}

func writeResetHelp(writer io.Writer, target string) {
	if target != "" {
		fmt.Fprintf(writer, "Usage: agent-gate reset %s [--apply]\n\n%s\n\nWithout --apply, nothing is changed.\n", target, resetTargetDescription(target))
		return
	}
	_, _ = io.WriteString(writer, `Usage: agent-gate reset [target] [--apply]

Without --apply, nothing is changed.

Targets:
  database  Delete audit databases and catalog state
	  state     Delete generated state and logs
  logs      Delete operational and fail-open logs
  cache     Delete cached data
  sockets   Delete runtime sockets and locks
  service   Reinstall the user service
	  binary    Reinstall the current binary
	  config    Delete Agent Gate configuration
	  hooks     Remove Agent Gate hook registrations

The default reset keeps configuration and provider hooks.
"agent-gate reset config --apply" deletes only Agent Gate configuration.
"agent-gate reset hooks --apply" removes only Agent Gate hook registrations.
Use "agent-gate reset --apply" to reset every target.
`)
}

func resetTargetDescription(target string) string {
	switch installer.ResetTarget(target) {
	case installer.ResetTargetDatabase:
		return "Deletes audit databases and catalog state."
	case installer.ResetTargetState:
		return "Deletes generated state and logs."
	case installer.ResetTargetLogs:
		return "Deletes operational and fail-open logs."
	case installer.ResetTargetCache:
		return "Deletes cached data."
	case installer.ResetTargetSockets:
		return "Deletes runtime sockets and locks."
	case installer.ResetTargetService:
		return "Reinstalls the user service."
	case installer.ResetTargetBinary:
		return "Reinstalls the current binary."
	case installer.ResetTargetConfig:
		return "Deletes only Agent Gate configuration."
	case installer.ResetTargetHooks:
		return "Removes only Agent Gate hook registrations."
	default:
		return ""
	}
}
