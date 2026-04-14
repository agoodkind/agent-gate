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
)

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

	// Hidden subcommand: start the background daemon.
	if len(os.Args) > 1 && os.Args[1] == "daemon" {
		log := openLog("daemon")
		if err := daemon.Run(log); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: daemon: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Everything else: hook mode (reads JSON from stdin).
	os.Exit(runHook())
}

// runClaudeWrapper connects to the daemon, acquires a per-session settings
// file, then execs the real claude with --settings pointing at it.
// On exit, releases the session so the daemon cleans up.
func runClaudeWrapper() int {
	log := openLog("wrapper")
	ctx := context.Background()
	wrapperID := fmt.Sprintf("%d", os.Getpid())

	cwd, _ := os.Getwd()
	log.Debug("wrapper starting", "pid", wrapperID, "cwd", cwd, "args", os.Args[1:])

	client, err := connectOrStartDaemon(ctx)
	if err != nil {
		log.Error("daemon unavailable", "err", err)
		return 1
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Warn("close client", "err", err)
		}
	}()

	sessionName := os.Getenv("AGENT_GATE_SESSION_NAME")

	resp, err := client.AcquireSession(wrapperID, sessionName)
	if err != nil {
		log.Error("acquire session failed", "err", err)
		return 1
	}

	defer func() {
		if err := client.ReleaseSession(wrapperID); err != nil {
			log.Warn("release session", "err", err)
		}
	}()

	args := append([]string{}, os.Args[1:]...)
	if resp.SettingsFile != "" && !containsFlag(args, "--settings") {
		args = append([]string{"--settings", resp.SettingsFile}, args...)
	}

	log.Info("launching claude",
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
			log.Info("claude exited", "code", exitErr.ExitCode())
			return exitErr.ExitCode()
		}
		log.Error("claude exec failed", "err", err)
		return 1
	}
	log.Info("claude exited", "code", 0)
	return 0
}

// connectOrStartDaemon connects to the daemon, starting it in the background
// if it is not already running.
func connectOrStartDaemon(ctx context.Context) (*daemon.Client, error) {
	client, err := daemon.Connect(ctx)
	if err == nil {
		return client, nil
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
		client, err = daemon.Connect(ctx)
		if err == nil {
			return client, nil
		}
		delay *= 2
	}

	return nil, fmt.Errorf("daemon did not become ready: %w", err)
}

// openLog returns a slog.Logger that writes JSON to the unified XDG state
// log file at ~/.local/state/agent-gate/agent-gate.jsonl.
// The component field distinguishes daemon vs wrapper entries.
func openLog(component string) *slog.Logger {
	logPath := filepath.Join(config.DefaultStateDir(), "agent-gate.jsonl")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return slog.New(slog.NewJSONHandler(io.Discard, nil))
	}
	return slog.New(slog.NewJSONHandler(f, nil)).With("component", component)
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
func runHook() int {
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

	// Load config from the XDG path, writing defaults on first run.
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: load config: %v\n", err)
		return 2
	}

	// Open the audit log (creates directories if needed).
	logger, err := audit.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: open audit logger: %v\n", err)
		return 2
	}
	defer logger.Close()

	// Dispatch to the hook handler.
	stdout, stderr, exitCode := hook.Handle(raw, cfg, logger)

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
