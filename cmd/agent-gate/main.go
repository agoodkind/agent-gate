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

	"github.com/mattn/go-isatty"

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
		if err := daemon.Run(slog.Default()); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: daemon: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Piped stdin + no subcommand args = hook mode (JSON from Claude/Cursor).
	if !isatty.IsTerminal(os.Stdin.Fd()) && len(os.Args) == 1 {
		os.Exit(runHook())
	}

	// Any other invocation: run in hook mode (reads stdin).
	os.Exit(runHook())
}

// runClaudeWrapper connects to the daemon, acquires a fake HOME for this
// process, then execs the real claude with HOME overridden and --settings
// pointing at the per-process settings.json (which has the per-session model
// injected). On exit, releases the session so the daemon cleans up.
func runClaudeWrapper() int {
	ctx := context.Background()
	wrapperID := fmt.Sprintf("%d", os.Getpid())

	// Connect to daemon (auto-start if not running).
	client, err := connectOrStartDaemon(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: daemon unavailable: %v\n", err)
		return 1
	}
	defer client.Close()

	// Session name from env (set by the user's shell or a wrapper script).
	sessionName := os.Getenv("AGENT_GATE_SESSION_NAME")

	resp, err := client.AcquireSession(wrapperID, sessionName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: acquire session: %v\n", err)
		return 1
	}

	defer func() {
		if err := client.ReleaseSession(wrapperID); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: release session: %v\n", err)
		}
	}()

	// Build args: pass through all user args, inject --settings for model.
	args := append([]string{}, os.Args[1:]...)
	if resp.SettingsFile != "" && !containsFlag(args, "--settings") {
		args = append([]string{"--settings", resp.SettingsFile}, args...)
	}

	claudeCmd := exec.Command(resp.RealClaude, args...)
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	// Override HOME so /model writes stay isolated to this process.
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if len(e) >= 5 && e[:5] == "HOME=" {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "HOME="+resp.FakeHome)
	claudeCmd.Env = env

	if err := claudeCmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "agent-gate: claude exited with error: %v\n", err)
		return 1
	}
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
	self, _ := os.Executable()
	daemonCmd := exec.Command(self, "daemon")
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil
	if startErr := daemonCmd.Start(); startErr != nil {
		return nil, fmt.Errorf("failed to start daemon: %w", startErr)
	}
	// Detach: let it run independently.
	go func() { _ = daemonCmd.Wait() }()

	// Retry connection with a short backoff.
	for range 10 {
		client, err = daemon.Connect(ctx)
		if err == nil {
			return client, nil
		}
	}

	return nil, fmt.Errorf("daemon did not become ready: %w", err)
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
