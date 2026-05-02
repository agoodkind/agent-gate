package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
		case "daemon":
			if len(os.Args) > 2 {
				switch os.Args[2] {
				case "status":
					os.Exit(runDaemonStatus())
				default:
					fmt.Fprintf(os.Stderr, "agent-gate: unknown daemon subcommand %q\n", os.Args[2])
					os.Exit(2)
				}
			}
		case "codex-hook":
			os.Exit(runHook(hook.SystemCodex))
		case "gemini-hook":
			os.Exit(runHook(hook.SystemGemini))
		case "logs":
			os.Exit(runLogs(os.Args[2:]))
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
			fmt.Fprintf(os.Stderr, "agent-gate: daemon: config load failed: %v\n", cfgErr)
			os.Exit(1)
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

	client, err := connectDaemon(ctx)
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
	claudeCmd.Env = append(os.Environ(), "AGENT_GATE_WRAPPER_ID="+wrapperID)

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

func connectDaemon(ctx context.Context) (*daemon.Client, error) {
	if _, statErr := os.Stat(config.DaemonSocketPath()); statErr != nil {
		return nil, fmt.Errorf("daemon socket unavailable at %s: %w", config.DaemonSocketPath(), statErr)
	}
	return daemon.Connect(ctx)
}

func runDaemonStatus() int {
	ctx := context.Background()
	client, err := connectDaemon(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: daemon unavailable: %v\n", err)
		return 1
	}
	defer func() { _ = client.Close() }()
	resp, err := client.Status()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: daemon status failed: %v\n", err)
		return 1
	}
	fmt.Printf("pid:              %d\n", resp.Pid)
	fmt.Printf("executable:       %s\n", resp.ExecutablePath)
	fmt.Printf("socket:           %s\n", resp.SocketPath)
	fmt.Printf("active_sessions:  %d\n", resp.ActiveSessions)
	fmt.Printf("version:          %s\n", resp.Version)
	fmt.Printf("commit:           %s\n", resp.Commit)
	fmt.Printf("dirty:            %s\n", resp.Dirty)
	fmt.Printf("buildHash:        %s\n", resp.BuildHash)
	return 0
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
	inner, closer := gklog.New(gklog.Config{
		Handlers: []slog.Handler{
			gklog.FileJSON(logPath, slog.LevelDebug, gklog.RotationConfig{
				MaxSizeMB: 5, MaxBackups: 0, MaxAgeDays: 0,
			}),
		},
	})
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

func runLogs(args []string) int {
	if len(args) == 0 || args[0] != "query" {
		fmt.Fprintln(os.Stderr, "usage: agent-gate logs query [--today] [--since 24h] [--system claude] [--decision block] [--rule NAME] [--json]")
		return 2
	}

	fs := flag.NewFlagSet("agent-gate logs query", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var filter audit.QueryFilter
	var today bool
	var since string
	var jsonOut bool
	fs.BoolVar(&today, "today", false, "show events since local midnight")
	fs.StringVar(&since, "since", "", "show events since duration or RFC3339 time")
	fs.StringVar(&filter.System, "system", "", "filter by system")
	fs.StringVar(&filter.SessionID, "session", "", "filter by session id")
	fs.StringVar(&filter.EventName, "event", "", "filter by event name")
	fs.StringVar(&filter.ToolName, "tool", "", "filter by tool name")
	fs.StringVar(&filter.Decision, "decision", "", "filter by decision")
	fs.StringVar(&filter.Rule, "rule", "", "filter by rule")
	fs.IntVar(&filter.Limit, "limit", 50, "maximum rows")
	fs.BoolVar(&jsonOut, "json", false, "print JSONL")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if today {
		now := time.Now()
		filter.Since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	if since != "" {
		t, err := parseSince(since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate logs query: invalid --since: %v\n", err)
			return 2
		}
		filter.Since = t
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate logs query: config load failed: %v\n", err)
		return 2
	}
	events, source, err := audit.Query(cfg, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate logs query: no audit output available: %v\n", err)
		return 1
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				fmt.Fprintf(os.Stderr, "agent-gate logs query: encode: %v\n", err)
				return 1
			}
		}
		return 0
	}
	printEventTable(source, events)
	return 0
}

func parseSince(s string) (time.Time, error) {
	if d, err := time.ParseDuration(s); err == nil {
		return time.Now().Add(-d), nil
	}
	return time.Parse(time.RFC3339, s)
}

func printEventTable(source string, events []audit.Event) {
	fmt.Printf("source=%s rows=%d\n", source, len(events))
	fmt.Printf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n", "time", "system", "decision", "event", "tool", "rules", "command")
	for _, event := range events {
		rules := "-"
		if len(event.Decision.RulesMatched) > 0 {
			rules = strings.Join(event.Decision.RulesMatched, ",")
		}
		cmd := event.Operation.Command
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		fmt.Printf("%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n",
			event.Time,
			event.System,
			event.Decision.Kind,
			event.EventName,
			event.ToolName,
			rules,
			cmd,
		)
	}
}

// runHook handles hook mode: read stdin, forward to daemon, mirror response.
func runHook(systemHint hook.HookSystem) int {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: read stdin: %v\n", err)
		return 2
	}
	ctx := context.Background()
	client, err := connectDaemon(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: daemon unavailable: %v\n", err)
		return 2
	}
	defer func() { _ = client.Close() }()

	cwd, _ := os.Getwd()
	resp, err := client.EvaluateHook(data, systemHint.String(), os.Getenv("AGENT_GATE_WRAPPER_ID"), cwd, os.Args, envFingerprint())
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: daemon EvaluateHook failed: %v\n", err)
		return 2
	}

	if len(resp.StdoutData) > 0 {
		if _, err := os.Stdout.Write(resp.StdoutData); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: write stdout: %v\n", err)
		}
	}
	if len(resp.StderrData) > 0 {
		if _, err := os.Stderr.Write(resp.StderrData); err != nil {
			_ = err
		}
	}

	return int(resp.ExitCode)
}

func envFingerprint() map[string]string {
	keys := []string{
		"AI_AGENT",
		"CLAUDE_CODE_ENTRYPOINT",
		"CODEX_CI",
		"CODEX_THREAD_ID",
		"COPILOT_OTEL_ENABLED",
		"COPILOT_OTEL_EXPORTER_TYPE",
		"COPILOT_OTEL_FILE_EXPORTER_PATH",
		"CURSOR_MODE",
		"CURSOR_VERSION",
		"CURSOR_WORKSPACE_NAME",
		"GEMINI_CLI",
		"VSCODE_IPC_HOOK",
		"VSCODE_PID",
	}
	out := make(map[string]string)
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			out[key] = v
		}
	}
	return out
}
