package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/version"
	"goodkind.io/gklog"
)

func writeUserLine(writer io.Writer, line string) {
	_, _ = io.WriteString(writer, line+"\n")
}

type commandName string

const (
	commandConfig     commandName = "config"
	commandCodexHook  commandName = "codex-hook"
	commandDaemon     commandName = "daemon"
	commandGeminiHook commandName = "gemini-hook"
	commandLogs       commandName = "logs"
	commandVersion    commandName = "version"
)

type daemonCommandName string

const (
	daemonCommandStatus daemonCommandName = "status"
)

// printVersion writes the build metadata used in log entries to stdout.
// Output mirrors the slog attrs from internal/version.Attrs so that what
// appears in audit logs is exactly what `agent-gate version` reports.
func printVersion(writer io.Writer) {
	_, _ = fmt.Fprintf(writer, "version:   %s\n", version.Version)
	_, _ = fmt.Fprintf(writer, "commit:    %s\n", version.Commit)
	_, _ = fmt.Fprintf(writer, "dirty:     %s\n", version.Dirty)
	_, _ = fmt.Fprintf(writer, "buildHash: %s\n", version.BuildHash())
}

func main() {
	slog.New(slog.NewJSONHandler(io.Discard, nil)).Info("agent-gate invocation", "argc", len(os.Args))
	// Hook panics are recovered in runHook so availability failures do not block.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: panic: %v\n", r)
			os.Exit(2)
		}
	}()

	if len(os.Args) > 1 {
		switch commandName(os.Args[1]) {
		case commandDaemon:
			if len(os.Args) > 2 {
				switch daemonCommandName(os.Args[2]) {
				case daemonCommandStatus:
					os.Exit(runDaemonStatus())
				default:
					fmt.Fprintf(os.Stderr, "agent-gate: unknown daemon subcommand %q\n", os.Args[2])
					os.Exit(2)
				}
			}
		case commandCodexHook:
			os.Exit(runHook(hook.SystemCodex))
		case commandGeminiHook:
			os.Exit(runHook(hook.SystemGemini))
		case commandLogs:
			os.Exit(runLogs(os.Args[2:]))
		case commandConfig:
			os.Exit(runConfig(os.Args[2:]))
		case commandVersion, "--version", "-v":
			printVersion(os.Stdout)
			return
		}
	}

	// Hidden subcommand: start the background daemon.
	if len(os.Args) > 1 && commandName(os.Args[1]) == commandDaemon {
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

func connectDaemon(ctx context.Context) (*daemon.Client, error) {
	socketPath := config.DaemonSocketPath()
	if _, statErr := os.Stat(socketPath); statErr != nil {
		slog.WarnContext(ctx, "daemon socket unavailable", slog.String("socket_path", socketPath), slog.Any("err", statErr))
		return nil, fmt.Errorf("daemon socket unavailable at %s: %w", socketPath, statErr)
	}
	return daemon.Connect(ctx)
}

type hookClient interface {
	EvaluateHook(rawJSON []byte, providerHint, cwd string, argv []string, env map[string]string) (*daemonpb.EvaluateHookResponse, error)
	Close() error
}

type hookRuntime struct {
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	args    []string
	connect func(context.Context) (hookClient, error)
	getwd   func() (string, error)
	env     func() map[string]string
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
	_, _ = fmt.Fprintf(os.Stdout, "pid:              %d\n", resp.Pid)
	_, _ = fmt.Fprintf(os.Stdout, "executable:       %s\n", resp.ExecutablePath)
	_, _ = fmt.Fprintf(os.Stdout, "socket:           %s\n", resp.SocketPath)
	_, _ = fmt.Fprintf(os.Stdout, "version:          %s\n", resp.Version)
	_, _ = fmt.Fprintf(os.Stdout, "commit:           %s\n", resp.Commit)
	_, _ = fmt.Fprintf(os.Stdout, "dirty:            %s\n", resp.Dirty)
	_, _ = fmt.Fprintf(os.Stdout, "buildHash:        %s\n", resp.BuildHash)
	return 0
}

func runConfig(args []string) int {
	if len(args) != 1 || args[0] != "check" {
		fmt.Fprintln(os.Stderr, "usage: agent-gate config check")
		return 2
	}
	if _, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: config check failed: %v\n", err)
		return 1
	}
	writeUserLine(os.Stdout, "agent-gate: config ok")
	return 0
}

// openLog returns a slog.Logger that writes JSON to the unified XDG state
// log file at ~/.local/state/agent-gate/agent-gate.jsonl, with rotation.
// The component field distinguishes daemon and hook-related log entries.
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
	_, _ = fmt.Fprintf(os.Stdout, "source=%s rows=%d\n", source, len(events))
	_, _ = fmt.Fprintf(os.Stdout, "%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n", "time", "system", "decision", "event", "tool", "rules", "command")
	for _, event := range events {
		rules := "-"
		if len(event.Decision.RulesMatched) > 0 {
			rules = strings.Join(event.Decision.RulesMatched, ",")
		}
		cmd := event.Operation.Command
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		_, _ = fmt.Fprintf(os.Stdout, "%-25s  %-8s  %-12s  %-12s  %-9s  %-24s  %s\n",
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
	runtime := hookRuntime{
		stdin:   os.Stdin,
		stdout:  os.Stdout,
		stderr:  os.Stderr,
		args:    os.Args,
		connect: defaultHookConnector,
		getwd:   os.Getwd,
		env:     envFingerprint,
	}
	return runHookWithRuntime(systemHint, runtime)
}

func defaultHookConnector(ctx context.Context) (hookClient, error) {
	return connectDaemon(ctx)
}

func runHookWithRuntime(systemHint hook.HookSystem, runtime hookRuntime) (exitCode int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			diagnostic := fmt.Sprintf("agent-gate: panic: %v", recovered)
			response := hook.FailOpenResponse(systemHint, "", diagnostic, hook.FailOpenReasonPanic)
			writeResponse(runtime.stdout, runtime.stderr, response)
			exitCode = response.ExitCode
		}
	}()

	data, err := io.ReadAll(runtime.stdin)
	if err != nil {
		diagnostic := fmt.Sprintf("agent-gate: read stdin: %v", err)
		response := hook.FailOpenResponse(systemHint, "", diagnostic, hook.FailOpenReasonStdinRead)
		writeResponse(runtime.stdout, runtime.stderr, response)
		return response.ExitCode
	}
	ctx := context.Background()
	client, err := runtime.connect(ctx)
	if err != nil {
		diagnostic := fmt.Sprintf("agent-gate: daemon unavailable: %v", err)
		response := hook.FailOpenResponse(systemHint, "", diagnostic, hook.FailOpenReasonDaemonUnavailable)
		writeResponse(runtime.stdout, runtime.stderr, response)
		return response.ExitCode
	}
	defer func() { _ = client.Close() }()

	cwd, _ := runtime.getwd()
	resp, err := client.EvaluateHook(data, systemHint.String(), cwd, runtime.args, runtime.env())
	if err != nil {
		diagnostic := fmt.Sprintf("agent-gate: daemon EvaluateHook failed: %v", err)
		response := hook.FailOpenResponse(systemHint, "", diagnostic, hook.FailOpenReasonRPCFailed)
		writeResponse(runtime.stdout, runtime.stderr, response)
		return response.ExitCode
	}

	if len(resp.StdoutData) > 0 {
		if _, err := runtime.stdout.Write(resp.StdoutData); err != nil {
			fmt.Fprintf(runtime.stderr, "agent-gate: write stdout: %v\n", err)
		}
	}
	if len(resp.StderrData) > 0 {
		if _, err := runtime.stderr.Write(resp.StderrData); err != nil {
			_ = err
		}
	}

	return int(resp.ExitCode)
}

func writeResponse(stdout io.Writer, stderr io.Writer, response hook.Response) {
	if len(response.Stdout) > 0 {
		if _, err := stdout.Write(response.Stdout); err != nil {
			fmt.Fprintf(stderr, "agent-gate: write stdout: %v\n", err)
		}
	}
	if len(response.Stderr) > 0 {
		if _, err := stderr.Write(response.Stderr); err != nil {
			_ = err
		}
	}
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
