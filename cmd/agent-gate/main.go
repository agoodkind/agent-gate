package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/version"
	"goodkind.io/gklog"
)

// printVersion writes the build metadata used in log entries to stdout.
// Output mirrors the slog attrs from internal/version.Attrs so that what
// appears in audit logs is exactly what `agent-gate version` reports.
func printVersion() {
	fmt.Printf("version:   %s\n", version.Version)
	fmt.Printf("commit:    %s\n", version.Commit)
	fmt.Printf("dirty:     %s\n", version.Dirty)
	fmt.Printf("buildHash: %s\n", version.BuildHash())
}

func main() {
	// Fail closed: any unrecovered panic exits 2, blocking the pending action.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: panic: %v\n", r)
			os.Exit(2)
		}
	}()

	// When installed as "claude" (symlink or rename), act as a transparent
	// wrapper that enforces per-process model isolation via the daemon.
	if filepath.Base(os.Args[0]) == "claude" {
		os.Exit(runClaudeWrapper())
	}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "codex-hook":
			os.Exit(runHook(hook.SystemCodex))
		case "gemini-hook":
			os.Exit(runHook(hook.SystemGemini))
		case "version", "--version", "-v":
			printVersion()
			return
		}
	}

	// Hidden subcommand: start the background daemon.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		log, closeLog := openLog("daemon")
		defer closeLog()
		cfg, cfgErr := config.Load()
		if cfgErr != nil {
			fmt.Fprintf(os.Stderr,
				"agent-gate: daemon: config load failed, using defaults: %v\n", cfgErr,
			)
			cfg = &config.Config{}
		}
		if err := daemon.Run(log, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: daemon: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Everything else: hook mode (reads JSON from stdin).
	os.Exit(runHook(hook.SystemUnknown))
}

// runClaudeWrapper connects to the daemon, acquires a per-session settings
// file, then execs the real claude with --settings pointing at it.
// On exit, releases the session so the daemon cleans up.
func runClaudeWrapper() int {
	log, closeLog := openLog("wrapper")
	defer closeLog()
	ctx := gklog.WithLogger(context.Background(), log)
	wrapperID := fmt.Sprintf("%d", os.Getpid())

	cwd, _ := os.Getwd()
	log.DebugContext(ctx, "wrapper starting", "pid", wrapperID, "cwd", cwd, "args", os.Args[1:])

	client, err := connectOrStartDaemon(ctx)
	if err != nil {
		log.ErrorContext(ctx, "daemon unavailable", "err", err)
		return 1
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.WarnContext(ctx, "close client", "err", err)
		}
	}()

	sessionName := os.Getenv("AGENT_GATE_SESSION_NAME")

	resp, err := client.AcquireSession(wrapperID, sessionName)
	if err != nil {
		log.ErrorContext(ctx, "acquire session failed", "err", err)
		return 1
	}

	defer func() {
		if err := client.ReleaseSession(wrapperID); err != nil {
			log.WarnContext(ctx, "release session", "err", err)
		}
	}()

	args := append([]string{}, os.Args[1:]...)
	if resp.SettingsFile != "" && !containsFlag(args, "--settings") {
		args = append([]string{"--settings", resp.SettingsFile}, args...)
	}

	log.InfoContext(ctx, "launching claude",
		"claude_bin", resp.RealClaude,
		"model", resp.Model,
		"settings", resp.SettingsFile,
		"cwd", cwd,
		"args", args,
	)

	claudeCmd := exec.Command(resp.RealClaude, args...)
	claudeCmd.Dir = cwd
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	if err := claudeCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			log.InfoContext(ctx, "claude exited", "code", exitErr.ExitCode())
			return exitErr.ExitCode()
		}
		log.ErrorContext(ctx, "claude exec failed", "err", err)
		return 1
	}
	log.InfoContext(ctx, "claude exited", "code", 0)
	return 0
}

// connectOrStartDaemon connects to the daemon, starting it in the background
// if it is not already running.
func connectOrStartDaemon(ctx context.Context) (*daemon.Client, error) {
	// gRPC NewClient is lazy and never fails on a missing socket. Probe the
	// Unix socket file directly so the auto-start path actually fires when
	// the daemon is not running.
	if _, statErr := os.Stat(config.DaemonSocketPath()); statErr == nil {
		client, err := daemon.Connect(ctx)
		if err == nil {
			return client, nil
		}
	}

	// Daemon not running - start it in the background.
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("failed to determine own path: %w", err)
	}
	daemonCmd := exec.Command(self, "daemon")
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil
	if startErr := daemonCmd.Start(); startErr != nil {
		return nil, fmt.Errorf("failed to start daemon: %w", startErr)
	}
	// Detach: let it run independently.
	go func() { _ = daemonCmd.Wait() }()

	// Retry with exponential backoff (50ms, 100ms, 200ms ... up to 1.6s).
	delay := 50 * time.Millisecond
	for range 6 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if _, statErr := os.Stat(config.DaemonSocketPath()); statErr != nil {
			delay *= 2
			continue
		}
		client, err := daemon.Connect(ctx)
		if err == nil {
			return client, nil
		}
		delay *= 2
	}

	return nil, fmt.Errorf("daemon did not become ready: %w", err)
}

// openLog returns a slog.Logger that writes JSON to the unified XDG state
// log file at ~/.local/state/agent-gate/agent-gate.jsonl, with rotation.
// The component field distinguishes daemon vs wrapper entries.
// The returned function closes the rotating log writer; call it before exit.
func openLog(component string) (*slog.Logger, func()) {
	logPath := filepath.Join(config.DefaultStateDir(), "agent-gate.jsonl")
	noClose := func() {}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil)), noClose
	}
	inner, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   logPath,
		Rotation:      gklog.RotationConfig{MaxSizeMB: 5, MaxBackups: 0, MaxAgeDays: 0},
		DisableStdout: true,
	})
	if err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil)), noClose
	}
	return inner.With("component", component), func() {
		if closer != nil {
			_ = closer.Close()
		}
	}
}

// containsFlag reports whether args contains the given flag string.
func containsFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// runHook handles hook mode: reads JSON from stdin, evaluates rules, writes output.
func runHook(forcedSystem hook.HookSystem) int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: read stdin: %v\n", err)
		return 2
	}

	// Tolerate empty stdin (e.g. manual invocation during testing).
	if len(data) == 0 {
		fmt.Fprintln(os.Stderr, "agent-gate: empty stdin - nothing to process")
		return 0
	}

	var raw hook.RawPayload
	if err := json.Unmarshal(data, &raw); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: parse stdin JSON: %v\n", err)
		return 2
	}

	// Load config. If loading fails (malformed TOML, compile errors, etc.)
	// we must fail OPEN. Failing closed would brick every tool invocation
	// the moment the config becomes unparseable, making it impossible to
	// edit the config back to a valid state. Log loudly and continue with
	// an empty config (no rules, default paths).
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"agent-gate: WARNING: config load failed, continuing with no rules: %v\n",
			err,
		)
		cfg = &config.Config{}
	}

	// Validation errors are reported but do NOT block. Same reasoning: a
	// stale field_path should not prevent the user from editing the config.
	if errs := hook.ValidateConfig(cfg); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "agent-gate: WARNING: config: %v\n", e)
		}
	}

	// Audit goes through the daemon. The daemon owns the LRU file-handle
	// cache and the async write worker. If the daemon is unreachable we
	// fall back to a discard sink so that hook enforcement still proceeds.
	var sink audit.Sink = audit.DiscardSink{}
	ctx := context.Background()
	if client, derr := connectOrStartDaemon(ctx); derr == nil {
		defer func() { _ = client.Close() }()
		sink = daemon.NewAuditSink(client)
	} else {
		fmt.Fprintf(os.Stderr,
			"agent-gate: WARNING: daemon unreachable, audit disabled: %v\n", derr,
		)
	}

	stdout, stderr, exitCode := hook.HandleWithOverride(ctx, raw, data, cfg, sink, forcedSystem)

	if len(stdout) > 0 {
		if _, err := os.Stdout.Write(stdout); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: write stdout: %v\n", err)
		}
	}
	if len(stderr) > 0 {
		if _, err := os.Stderr.Write(stderr); err != nil {
			_ = err
		}
	}

	return exitCode
}
