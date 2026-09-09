package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
}

func defaultResetDependencies() resetDependencies {
	return resetDependencies{resolveExecutable: os.Executable, prepareReset: installer.PrepareReset, applyReset: installer.ApplyReset, install: defaultInstallDependencies()}
}

func runReset(args []string) int { return runResetWithDependencies(args, defaultResetDependencies()) }

func runResetWithDependencies(args []string, dependencies resetDependencies) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-gate reset")
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	lockPath := filepath.Join(filepath.Dir(config.RuntimeDir()), "agent-gate-reset.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return reportResetError("lock", err)
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryLockContext(ctx, 100*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		return reportResetError("lock", errors.Join(err, ctx.Err()))
	}
	defer func() { _ = lock.Close() }()
	path, err := dependencies.resolveExecutable()
	if err != nil {
		return reportResetError("prepare", err)
	}
	path, err = installer.CanonicalExecutablePath(path)
	if err != nil {
		return reportResetError("prepare", err)
	}
	options, err := resetOptions(path)
	if err != nil {
		return reportResetError("prepare", err)
	}
	plan, err := dependencies.prepareReset(options)
	if err != nil {
		return reportResetError("prepare", err)
	}
	if err := dependencies.applyReset(ctx, plan); err != nil {
		return reportResetError("apply", err)
	}
	code := runInstallWithDependencies([]string{"service", "--bin-path", path}, dependencies.install)
	if code != 0 {
		fmt.Fprintln(os.Stderr, "agent-gate reset: reinstall failed; executable restored, old data removed")
	}
	return code
}

func reportResetError(stage string, err error) int {
	fmt.Fprintf(os.Stderr, "agent-gate reset: %s: %v\n", stage, err)
	return 1
}

func resetOptions(path string) (installer.ResetOptions, error) {
	var cfg config.Config
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
	home, err := os.UserHomeDir()
	if err != nil {
		return installer.ResetOptions{}, err
	}
	return installer.ResetOptions{
		ExecutablePath: path, Service: installer.ServiceOptions{BinPath: path, Stdout: os.Stdout},
		StateDir: config.DefaultStateDir(), CacheDir: config.DefaultCacheDir(), RuntimeDir: config.RuntimeDir(), ConfigDir: config.DefaultConfigDir(), Audit: cfg.AuditCatalogOptions(),
		PreservedPaths: []string{config.Path(), filepath.Join(home, ".claude", "settings.json"), filepath.Join(home, ".codex", "config.toml"), filepath.Join(home, ".cursor", "hooks.json"), filepath.Join(home, ".gemini", "settings.json"), filepath.Join(home, ".copilot", "hooks", "agent-gate.json")},
	}, nil
}
