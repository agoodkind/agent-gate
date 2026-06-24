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
	"syscall"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/hook"
	"goodkind.io/agent-gate/internal/intake"
	"goodkind.io/agent-gate/internal/telemetry"
	updater "goodkind.io/agent-gate/internal/update"
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
	commandQuery      commandName = "query"
	commandUpdate     commandName = "update"
	commandVersion    commandName = "version"
)

type daemonCommandName string

const (
	daemonCommandStatus daemonCommandName = "status"
)

type queryCommandName string

const (
	queryCommandDecisions queryCommandName = "decisions"
	queryCommandSeen      queryCommandName = "seen"
)

type updateCommandName string

const (
	updateCommandApply  updateCommandName = "apply"
	updateCommandCheck  updateCommandName = "check"
	updateCommandStatus updateCommandName = "status"
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
		case commandQuery:
			os.Exit(runQuery(os.Args[2:]))
		case commandConfig:
			os.Exit(runConfig(os.Args[2:]))
		case commandUpdate:
			os.Exit(runUpdate(os.Args[2:]))
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
		telemCloser, telemErr := telemetry.Setup(telemetry.Options{
			OTLPEndpoint:      cfg.Telemetry.OTLPEndpoint,
			SlowOpThresholdMs: cfg.Telemetry.SlowOpThresholdMs,
		})
		if telemErr != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: daemon: telemetry setup failed: %v\n", telemErr)
			os.Exit(1)
		}
		defer func() { _ = telemCloser.Close() }()
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
	if len(args) == 1 && args[0] == "check" {
		if _, err := config.Load(); err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate: config check failed: %v\n", err)
			return 1
		}
		writeUserLine(os.Stdout, "agent-gate: config ok")
		return 0
	}
	if len(args) > 0 && args[0] == "ensure-defaults" {
		return runConfigEnsureDefaults(args[1:])
	}
	fmt.Fprintln(os.Stderr, "usage: agent-gate config check | ensure-defaults [--auto-update check|apply|off]")
	return 2
}

func runConfigEnsureDefaults(args []string) int {
	fs := flag.NewFlagSet("config ensure-defaults", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var autoUpdate string
	fs.StringVar(&autoUpdate, "auto-update", "", "override update mode: check, apply, or off")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate config ensure-defaults: %v\n", err)
		return 2
	}
	configPath, err := config.EnsureDefaults(config.EnsureDefaultsOptions{
		AutoUpdateMode: autoUpdate,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: config ensure-defaults failed: %v\n", err)
		return 1
	}
	writeUserLine(os.Stdout, "agent-gate: defaults ensured at "+configPath)
	return 0
}

func runUpdate(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-gate update check|apply|status")
		return 2
	}
	switch updateCommandName(args[0]) {
	case updateCommandCheck:
		return runUpdateCheck(args[1:])
	case updateCommandApply:
		return runUpdateApply(args[1:])
	case updateCommandStatus:
		return runUpdateStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "agent-gate update: unknown subcommand %q\n", args[0])
		return 2
	}
}

func runUpdateCheck(args []string) int {
	fs := flag.NewFlagSet("update check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate update check: %v\n", err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update check config load failed: %v\n", err)
		return 1
	}
	result, err := updater.Check(context.Background(), updater.Options{Config: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update check failed: %v\n", err)
		return 1
	}
	writeUserLine(os.Stdout, "current version: "+result.CurrentVersion)
	writeUserLine(os.Stdout, "latest tag:      "+result.LatestTag)
	writeUserLine(os.Stdout, "asset:           "+result.AssetName)
	if result.UpdateAvailable {
		writeUserLine(os.Stdout, "update available: yes")
	} else {
		writeUserLine(os.Stdout, "update available: no")
	}
	return 0
}

func runUpdateApply(args []string) int {
	fs := flag.NewFlagSet("update apply", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var dryRun bool
	fs.BoolVar(&dryRun, "dry-run", false, "download and verify without installing")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate update apply: %v\n", err)
		return 2
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update apply config load failed: %v\n", err)
		return 1
	}
	result, err := updater.Apply(context.Background(), updater.Options{Config: cfg, DryRun: dryRun})
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update apply failed: %v\n", err)
		return 1
	}
	if !result.UpdateAvailable {
		writeUserLine(os.Stdout, "agent-gate: already current")
		return 0
	}
	if dryRun {
		writeUserLine(os.Stdout, "agent-gate: update apply dry run ok")
		return 0
	}
	if err := restartManagedDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update apply restart failed: %v\n", err)
		return 1
	}
	writeUserLine(os.Stdout, "agent-gate: update applied and daemon restarted")
	return 0
}

func runUpdateStatus(args []string) int {
	fs := flag.NewFlagSet("update status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate update status: %v\n", err)
		return 2
	}
	state, err := updater.LoadState("")
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate: update status failed: %v\n", err)
		return 1
	}
	writeUserLine(os.Stdout, "current version:   "+version.Version)
	writeUserLine(os.Stdout, "current commit:    "+version.Commit)
	writeUserLine(os.Stdout, "current buildHash: "+version.BuildHash())
	if !state.LastCheckAt.IsZero() {
		writeUserLine(os.Stdout, "last check:        "+state.LastCheckAt.Format(time.RFC3339))
	}
	if !state.NextCheckAt.IsZero() {
		writeUserLine(os.Stdout, "next check:        "+state.NextCheckAt.Format(time.RFC3339))
	}
	if state.LatestTag != "" {
		writeUserLine(os.Stdout, "latest tag:        "+state.LatestTag)
	}
	if state.AppliedTag != "" {
		writeUserLine(os.Stdout, "applied tag:       "+state.AppliedTag)
	}
	if state.LastResult != "" {
		writeUserLine(os.Stdout, "last result:       "+state.LastResult)
	}
	if state.LastError != "" {
		writeUserLine(os.Stdout, "last error:        "+state.LastError)
	}
	return 0
}

func restartManagedDaemon() error {
	ctx := context.Background()
	client, err := connectDaemon(ctx)
	if err != nil {
		return nil
	}
	defer func() { _ = client.Close() }()
	status, err := client.Status()
	if err != nil {
		slog.Warn("restart managed daemon status lookup failed", slog.Any("err", err))
		return fmt.Errorf("read daemon status before restart: %w", err)
	}
	process, err := os.FindProcess(int(status.Pid))
	if err != nil {
		slog.Warn("restart managed daemon process lookup failed", slog.Int64("pid", status.Pid), slog.Any("err", err))
		return fmt.Errorf("find daemon process: %w", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		slog.Warn("restart managed daemon signal failed", slog.Int64("pid", status.Pid), slog.Any("err", err))
		return fmt.Errorf("signal daemon: %w", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		restartedClient, connectErr := connectDaemon(ctx)
		if connectErr != nil {
			continue
		}
		restartedStatus, statusErr := restartedClient.Status()
		_ = restartedClient.Close()
		if statusErr != nil {
			continue
		}
		if restartedStatus.Pid != status.Pid {
			return nil
		}
	}
	slog.Warn("restart managed daemon timed out", slog.Int64("old_pid", status.Pid))
	return fmt.Errorf("daemon did not restart with a new pid")
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

func runQuery(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: agent-gate query seen|decisions [flags]")
		return 2
	}
	switch queryCommandName(args[0]) {
	case queryCommandSeen:
		return runSeenQuery(args[1:])
	case queryCommandDecisions:
		return runDecisionQuery(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "agent-gate query: unknown subcommand %q\n", args[0])
		return 2
	}
}

type sharedQueryFlags struct {
	today   bool
	since   string
	until   string
	jsonOut bool
}

func registerSharedQueryFlags(fs *flag.FlagSet, shared *sharedQueryFlags, system *string, session *string, event *string, tool *string, limit *int) {
	fs.BoolVar(&shared.today, "today", false, "show events since local midnight")
	fs.StringVar(&shared.since, "since", "", "show events since duration or RFC3339 time")
	fs.StringVar(&shared.until, "until", "", "show events until duration or RFC3339 time")
	fs.StringVar(system, "system", "", "filter by system")
	fs.StringVar(session, "session", "", "filter by session id")
	fs.StringVar(event, "event", "", "filter by event name")
	fs.StringVar(tool, "tool", "", "filter by tool name")
	fs.IntVar(limit, "limit", 50, "maximum rows")
	fs.BoolVar(&shared.jsonOut, "json", false, "print JSONL")
}

func applySharedAuditQueryFlags(shared sharedQueryFlags, filter *audit.QueryFilter, command string) bool {
	since, until, ok := parseSharedQueryRange(shared, command)
	if !ok {
		return false
	}
	filter.Since = since
	filter.Until = until
	return true
}

func applySharedSeenQueryFlags(shared sharedQueryFlags, filter *intake.QueryFilter, command string) bool {
	since, until, ok := parseSharedQueryRange(shared, command)
	if !ok {
		return false
	}
	filter.Since = since
	filter.Until = until
	return true
}

func parseSharedQueryRange(shared sharedQueryFlags, command string) (time.Time, time.Time, bool) {
	var since time.Time
	var until time.Time
	if shared.today {
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	}
	if shared.since != "" {
		t, err := parseQueryTime(shared.since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate %s: invalid --since: %v\n", command, err)
			return time.Time{}, time.Time{}, false
		}
		since = t
	}
	if shared.until != "" {
		t, err := parseQueryTime(shared.until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agent-gate %s: invalid --until: %v\n", command, err)
			return time.Time{}, time.Time{}, false
		}
		until = t
	}
	return since, until, true
}

func runDecisionQuery(args []string) int {
	fs := flag.NewFlagSet("agent-gate query decisions", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var filter audit.QueryFilter
	var shared sharedQueryFlags
	registerSharedQueryFlags(fs, &shared, &filter.System, &filter.SessionID, &filter.EventName, &filter.ToolName, &filter.Limit)
	fs.StringVar(&filter.Decision, "decision", "", "filter by decision")
	fs.StringVar(&filter.Rule, "rule", "", "filter by rule")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !applySharedAuditQueryFlags(shared, &filter, "query decisions") {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate query decisions: config load failed: %v\n", err)
		return 2
	}
	events, source, err := audit.Query(cfg, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate query decisions: no audit output available: %v\n", err)
		return 1
	}
	if shared.jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				fmt.Fprintf(os.Stderr, "agent-gate query decisions: encode: %v\n", err)
				return 1
			}
		}
		return 0
	}
	printEventTable(source, events)
	return 0
}

func runSeenQuery(args []string) int {
	fs := flag.NewFlagSet("agent-gate query seen", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var filter intake.QueryFilter
	var shared sharedQueryFlags
	registerSharedQueryFlags(fs, &shared, &filter.System, &filter.SessionID, &filter.EventName, &filter.ToolName, &filter.Limit)
	fs.StringVar(&filter.DeferredState, "state", "", "filter by deferred replay state")
	fs.StringVar(&filter.EventID, "event-id", "", "filter by durable event id")
	fs.BoolVar(&filter.IncludeNormalized, "include-normalized", false, "include normalized payload JSON")
	fs.BoolVar(&filter.IncludeEnv, "include-env", false, "include environment fingerprint")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !applySharedSeenQueryFlags(shared, &filter, "query seen") {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate query seen: config load failed: %v\n", err)
		return 2
	}
	result, err := intake.Query(context.Background(), cfg, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-gate query seen: %v\n", err)
		return 1
	}
	if shared.jsonOut {
		if result.Note != "" {
			fmt.Fprintf(os.Stderr, "agent-gate query seen: %s\n", result.Note)
		}
		enc := json.NewEncoder(os.Stdout)
		for _, record := range result.Records {
			if err := enc.Encode(record); err != nil {
				fmt.Fprintf(os.Stderr, "agent-gate query seen: encode: %v\n", err)
				return 1
			}
		}
		return 0
	}
	printSeenTable(result)
	return 0
}

func parseQueryTime(s string) (time.Time, error) {
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

func printSeenTable(result intake.QueryResult) {
	_, _ = fmt.Fprintf(os.Stdout, "source=%s rows=%d\n", result.Source, len(result.Records))
	if result.Note != "" {
		_, _ = fmt.Fprintf(os.Stdout, "note=%s\n", result.Note)
	}
	_, _ = fmt.Fprintf(os.Stdout, "%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %s\n", "recorded_at", "system", "state", "event", "tool", "session", "command")
	for _, record := range result.Records {
		cmd := record.Operation.Command
		if len(cmd) > 80 {
			cmd = cmd[:77] + "..."
		}
		_, _ = fmt.Fprintf(os.Stdout, "%-25s  %-8s  %-12s  %-12s  %-9s  %-10s  %s\n",
			record.RecordedAt,
			record.System,
			record.Deferred.State,
			record.EventName,
			record.ToolName,
			record.SessionID,
			cmd,
		)
	}
}

// runHook handles hook mode: read stdin, forward to daemon, mirror response.
func runHook(systemHint hook.System) int {
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

func runHookWithRuntime(systemHint hook.System, runtime hookRuntime) (exitCode int) {
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
