package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/auditmaintenance"
	"goodkind.io/agent-gate/internal/config"
)

var errScheduledMaintenance = errors.New("scheduled maintenance failed")

type controlledMaintenanceTimer struct {
	duration time.Duration
	ticks    chan time.Time
	stopped  chan struct{}
	stopOnce sync.Once
}

func (timer *controlledMaintenanceTimer) Tick(at time.Time) {
	select {
	case <-timer.stopped:
		return
	default:
		timer.ticks <- at
	}
}

type maintenanceSchedulerFixture struct {
	server       *Server
	configPath   string
	databasePath string
	now          time.Time
	timers       chan *controlledMaintenanceTimer
	runs         chan time.Time
	runErrors    chan error
	writes       chan time.Time
	logs         *bytes.Buffer
}

func TestMaintenanceSchedulerWaitsFullIntervalAfterReadiness(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	assertNoMaintenanceRun(t, fixture.runs)

	fixture.server.StartMaintenanceScheduler(context.Background())
	timer := receiveMaintenanceTimer(t, fixture.timers)
	if timer.duration != time.Hour {
		t.Fatalf("first timer duration = %s, want 1h", timer.duration)
	}
	if persisted := receiveMaintenanceTime(t, fixture.writes); !persisted.Equal(fixture.now.Add(time.Hour)) {
		t.Fatalf("persisted next attempt = %s, want %s", persisted, fixture.now.Add(time.Hour))
	}
	assertNextAttempt(t, fixture.databasePath, fixture.now.Add(time.Hour))
	assertNoMaintenanceRun(t, fixture.runs)

	timer.Tick(fixture.now.Add(time.Hour))
	if plannedAt := receiveMaintenanceTime(t, fixture.runs); !plannedAt.Equal(fixture.now.Add(time.Hour)) {
		t.Fatalf("maintenance planned at = %s, want %s", plannedAt, fixture.now.Add(time.Hour))
	}
}

func TestDaemonStartupNeverCallsMaintenance(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	assertNoMaintenanceRun(t, fixture.runs)
	assertNoMaintenanceTimer(t, fixture.timers)
	assertNoNextAttempt(t, fixture.databasePath)
}

func TestOverdueRecordDoesNotTriggerStartupMaintenance(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	insertOverdueMaintenanceRun(t, fixture)
	fixture.server.Close()
	fixture.server = newFixtureServer(t, fixture)
	status := readMaintenanceStatus(t, fixture)
	if !status.Overdue {
		t.Fatal("maintenance overdue = false, want true")
	}
	assertNoMaintenanceRun(t, fixture.runs)
	assertNoMaintenanceTimer(t, fixture.timers)
}

func TestMaintenanceReloadResetsFullInterval(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	fixture.server.StartMaintenanceScheduler(context.Background())
	oldTimer := receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)

	fixture.now = fixture.now.Add(15 * time.Minute)
	writeMaintenanceConfig(t, fixture.configPath, fixture.databasePath, 2*time.Hour)
	if err := fixture.server.reloadConfig(t.Context()); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	newTimer := receiveMaintenanceTimer(t, fixture.timers)
	if newTimer.duration != 2*time.Hour {
		t.Fatalf("reloaded timer duration = %s, want 2h", newTimer.duration)
	}
	if persisted := receiveMaintenanceTime(t, fixture.writes); !persisted.Equal(fixture.now.Add(2 * time.Hour)) {
		t.Fatalf("reloaded next attempt = %s, want %s", persisted, fixture.now.Add(2*time.Hour))
	}
	oldTimer.Tick(fixture.now.Add(time.Hour))
	assertNoMaintenanceRun(t, fixture.runs)
}

func TestMaintenanceReloadCancellationWinsReadyOldTick(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	writeStarted := make(chan struct{})
	var writeCalls atomic.Int64
	writeNextAttempt := fixture.server.maintenanceWriteNextAttempt
	fixture.server.maintenanceWriteNextAttempt = func(
		ctx context.Context,
		path string,
		nextAttempt time.Time,
	) error {
		if writeCalls.Add(1) == 1 {
			close(writeStarted)
			<-ctx.Done()
			return ctx.Err()
		}
		err := writeNextAttempt(ctx, path, nextAttempt)
		fixture.writes <- nextAttempt
		return err
	}
	fixture.server.StartMaintenanceScheduler(context.Background())
	oldTimer := receiveMaintenanceTimer(t, fixture.timers)
	<-writeStarted

	fixture.now = fixture.now.Add(15 * time.Minute)
	writeMaintenanceConfig(t, fixture.configPath, fixture.databasePath, 2*time.Hour)
	oldTimer.ticks <- fixture.now.Add(time.Hour)
	if err := fixture.server.reloadConfig(t.Context()); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	assertNoMaintenanceRun(t, fixture.runs)
}

func TestReloadCannotRunCanceledOldTickAfterRuntimeSwap(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	writeStarted := make(chan struct{})
	writeCanceled := make(chan struct{})
	allowExit := make(chan struct{})
	var allowExitOnce sync.Once
	t.Cleanup(func() { allowExitOnce.Do(func() { close(allowExit) }) })
	var writeCalls atomic.Int64
	writeNextAttempt := fixture.server.maintenanceWriteNextAttempt
	fixture.server.maintenanceWriteNextAttempt = func(
		ctx context.Context,
		path string,
		nextAttempt time.Time,
	) error {
		if writeCalls.Add(1) == 1 {
			close(writeStarted)
			<-ctx.Done()
			close(writeCanceled)
			<-allowExit
			return ctx.Err()
		}
		return writeNextAttempt(ctx, path, nextAttempt)
	}
	fixture.server.StartMaintenanceScheduler(context.Background())
	oldTimer := receiveMaintenanceTimer(t, fixture.timers)
	<-writeStarted

	writeMaintenanceConfig(t, fixture.configPath, fixture.databasePath, 2*time.Hour)
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- fixture.server.reloadConfig(t.Context()) }()
	<-writeCanceled
	if got := fixture.server.runtime.Load().cfg.AuditStoragePolicy().MaintenanceInterval; got != time.Hour {
		t.Fatalf("runtime interval while old scheduler exits = %s, want 1h", got)
	}
	assertCommandDecision(t, fixture.server, "alpha", 0, "block-alpha")
	oldTimer.Tick(fixture.now.Add(time.Hour))
	select {
	case err := <-reloadDone:
		t.Fatalf("reload returned before old scheduler exited: %v", err)
	default:
	}
	allowExitOnce.Do(func() { close(allowExit) })
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	newTimer := receiveMaintenanceTimer(t, fixture.timers)
	if newTimer.duration != 2*time.Hour {
		t.Fatalf("reloaded timer duration = %s, want 2h", newTimer.duration)
	}
	_ = receiveMaintenanceTime(t, fixture.writes)
	if got := fixture.server.runtime.Load().cfg.AuditStoragePolicy().MaintenanceInterval; got != 2*time.Hour {
		t.Fatalf("runtime interval after reload = %s, want 2h", got)
	}
	assertCommandDecision(t, fixture.server, "alpha", 0, "block-alpha")
	assertNoMaintenanceRun(t, fixture.runs)
}

func TestMaintenanceRestartResetsFullInterval(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	fixture.server.StartMaintenanceScheduler(context.Background())
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	fixture.server.Close()

	fixture.now = fixture.now.Add(20 * time.Minute)
	fixture.server = newFixtureServer(t, fixture)
	fixture.server.StartMaintenanceScheduler(context.Background())
	restartedTimer := receiveMaintenanceTimer(t, fixture.timers)
	if restartedTimer.duration != time.Hour {
		t.Fatalf("restarted timer duration = %s, want 1h", restartedTimer.duration)
	}
	if persisted := receiveMaintenanceTime(t, fixture.writes); !persisted.Equal(fixture.now.Add(time.Hour)) {
		t.Fatalf("restarted next attempt = %s, want %s", persisted, fixture.now.Add(time.Hour))
	}
}

func TestMaintenanceRestartKeepsOverdueStatus(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	insertOverdueMaintenanceRun(t, fixture)
	fixture.server.Close()
	fixture.server = newFixtureServer(t, fixture)
	fixture.server.StartMaintenanceScheduler(context.Background())
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	if status := readMaintenanceStatus(t, fixture); !status.Overdue {
		t.Fatal("maintenance overdue after restart = false, want true")
	}
}

func TestMaintenanceReloadKeepsOverdueStatus(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	insertOverdueMaintenanceRun(t, fixture)
	fixture.server.StartMaintenanceScheduler(context.Background())
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	writeMaintenanceConfig(t, fixture.configPath, fixture.databasePath, 2*time.Hour)
	if err := fixture.server.reloadConfig(t.Context()); err != nil {
		t.Fatalf("reloadConfig: %v", err)
	}
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	if status := readMaintenanceStatus(t, fixture); !status.Overdue {
		t.Fatal("maintenance overdue after reload = false, want true")
	}
}

func TestMaintenanceFailureWaitsUntilNextInterval(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	fixture.runErrors <- errScheduledMaintenance
	startedAt := fixture.now
	completedAt := startedAt.Add(90 * time.Minute)
	fixture.server.maintenanceRunner = func(
		_ context.Context,
		_ string,
		_ config.AuditStoragePolicy,
		plannedAt time.Time,
	) (auditmaintenance.Result, error) {
		fixture.runs <- plannedAt
		fixture.now = completedAt
		return auditmaintenance.Result{}, <-fixture.runErrors
	}
	fixture.server.StartMaintenanceScheduler(context.Background())
	firstTimer := receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	firstTimer.Tick(startedAt.Add(time.Hour))
	_ = receiveMaintenanceTime(t, fixture.runs)
	secondTimer := receiveMaintenanceTimer(t, fixture.timers)
	if secondTimer.duration != time.Hour {
		t.Fatalf("retry timer duration = %s, want 1h", secondTimer.duration)
	}
	if persisted := receiveMaintenanceTime(t, fixture.writes); !persisted.Equal(completedAt.Add(time.Hour)) {
		t.Fatalf("post-run next attempt = %s, want %s", persisted, completedAt.Add(time.Hour))
	}
	assertNoMaintenanceRun(t, fixture.runs)
	assertMaintenanceFailureLeavesGRPCEnforcementActive(t, fixture.server)

	fixture.runErrors <- nil
	secondTimer.Tick(completedAt.Add(time.Hour))
	_ = receiveMaintenanceTime(t, fixture.runs)
}

func assertMaintenanceFailureLeavesGRPCEnforcementActive(t *testing.T, server *Server) {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	daemonpb.RegisterAgentGateDServer(grpcServer, server)
	serveResult := serveAsync(grpcServer, listener)
	t.Cleanup(grpcServer.Stop)

	connection, err := grpc.NewClient(
		"passthrough:///maintenance-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
	)
	if err != nil {
		t.Fatalf("create daemon client: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	response, err := daemonpb.NewAgentGateDClient(connection).EvaluateHook(
		t.Context(),
		&daemonpb.EvaluateHookRequest{
			RawJson:      []byte(`{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"alpha"}}`),
			ProviderHint: "codex",
			Cwd:          t.TempDir(),
			EnvFingerprint: map[string]string{
				"CODEX_THREAD_ID": "maintenance-failure-test",
			},
		},
	)
	if err != nil {
		t.Fatalf("EvaluateHook after maintenance failure: %v", err)
	}
	if !strings.Contains(string(response.StdoutData), `"permissionDecision":"deny"`) {
		t.Fatalf("enforcement response after maintenance failure = %q", response.StdoutData)
	}
	grpcServer.Stop()
	if err := <-serveResult; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("gRPC server after maintenance failure: %v", err)
	}
}

func TestMaintenanceDeadlinePersistsOnlyAfterReadiness(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	assertNoNextAttempt(t, fixture.databasePath)
	fixture.server.StartMaintenanceScheduler(context.Background())
	_ = receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	assertNextAttempt(t, fixture.databasePath, fixture.now.Add(time.Hour))
}

func TestMaintenanceMetadataFailureKeepsInMemoryTimer(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	fixture.server.maintenanceWriteNextAttempt = func(context.Context, string, time.Time) error {
		fixture.writes <- time.Time{}
		return errors.New("metadata unavailable")
	}
	fixture.server.StartMaintenanceScheduler(context.Background())
	timer := receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	timer.Tick(fixture.now.Add(time.Hour))
	_ = receiveMaintenanceTime(t, fixture.runs)
}

func TestMaintenanceSchedulerLogsRunIdentifierAndCommittedCounts(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	type outcome struct {
		result auditmaintenance.Result
		err    error
	}
	outcomes := make(chan outcome, 3)
	fixture.server.maintenanceRunner = func(
		_ context.Context,
		_ string,
		_ config.AuditStoragePolicy,
		plannedAt time.Time,
	) (auditmaintenance.Result, error) {
		fixture.runs <- plannedAt
		current := <-outcomes
		return current.result, current.err
	}
	outcomes <- outcome{result: auditmaintenance.Result{
		RunID: "run-success", Result: "success", DetailGraphs: 2,
		SummaryGraphs: 1, ReclaimedBytes: 4096,
	}}
	outcomes <- outcome{result: auditmaintenance.Result{
		RunID: "run-deferred", Result: "deferred", Err: auditmaintenance.ErrMaintenanceBusy,
	}}
	outcomes <- outcome{
		result: auditmaintenance.Result{RunID: "run-failed", Result: "failed"},
		err:    errScheduledMaintenance,
	}

	fixture.server.StartMaintenanceScheduler(context.Background())
	timer := receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	for i := 0; i < 3; i++ {
		timer.Tick(fixture.now.Add(time.Duration(i+1) * time.Hour))
		_ = receiveMaintenanceTime(t, fixture.runs)
		timer = receiveMaintenanceTimer(t, fixture.timers)
		_ = receiveMaintenanceTime(t, fixture.writes)
	}
	logs := fixture.logs.String()
	for _, required := range []string{
		"audit maintenance scheduled run completed", "run_id=run-success",
		"result=success", "detail_graphs=2", "summary_graphs=1", "reclaimed_bytes=4096",
		"audit maintenance scheduled run deferred", "run_id=run-deferred",
		"audit maintenance scheduled run failed", "run_id=run-failed",
	} {
		if !strings.Contains(logs, required) {
			t.Fatalf("scheduler logs missing %q: %s", required, logs)
		}
	}
}

func TestMaintenanceInvalidReloadKeepsTiming(t *testing.T) {
	fixture := newMaintenanceSchedulerFixture(t, time.Hour)
	fixture.server.StartMaintenanceScheduler(context.Background())
	timer := receiveMaintenanceTimer(t, fixture.timers)
	_ = receiveMaintenanceTime(t, fixture.writes)
	writeConfig(t, fixture.configPath, "[audit.storage]\nmaintenance_interval = \"invalid\"\n")
	if err := fixture.server.reloadConfig(t.Context()); err == nil {
		t.Fatal("reloadConfig succeeded, want invalid maintenance interval")
	}
	assertNoMaintenanceTimer(t, fixture.timers)
	timer.Tick(fixture.now.Add(time.Hour))
	_ = receiveMaintenanceTime(t, fixture.runs)
}

func newMaintenanceSchedulerFixture(
	t *testing.T,
	interval time.Duration,
) *maintenanceSchedulerFixture {
	t.Helper()
	setDaemonTestDirs(t)
	fixture := &maintenanceSchedulerFixture{
		server: nil, configPath: config.Path(), databasePath: filepath.Join(t.TempDir(), "audit.db"),
		now:    time.Date(2026, time.August, 12, 9, 0, 0, 0, time.UTC),
		timers: make(chan *controlledMaintenanceTimer, 10), runs: make(chan time.Time, 10),
		runErrors: make(chan error, 10), writes: make(chan time.Time, 10),
		logs: &bytes.Buffer{},
	}
	writeMaintenanceConfig(t, fixture.configPath, fixture.databasePath, interval)
	fixture.server = newFixtureServer(t, fixture)
	t.Cleanup(func() { fixture.server.Close() })
	return fixture
}

func newFixtureServer(t *testing.T, fixture *maintenanceSchedulerFixture) *Server {
	t.Helper()
	cfg, err := config.LoadExisting(fixture.configPath)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(fixture.logs, nil))
	server, err := New(logger, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	server.maintenanceNow = func() time.Time { return fixture.now }
	server.maintenanceTimerFactory = func(duration time.Duration) maintenanceTimer {
		timer := &controlledMaintenanceTimer{
			duration: duration, ticks: make(chan time.Time, 1),
			stopped: make(chan struct{}), stopOnce: sync.Once{},
		}
		fixture.timers <- timer
		return maintenanceTimer{
			channel: timer.ticks,
			stop:    func() { timer.stopOnce.Do(func() { close(timer.stopped) }) },
		}
	}
	server.maintenanceRunner = func(
		_ context.Context,
		_ string,
		_ config.AuditStoragePolicy,
		plannedAt time.Time,
	) (auditmaintenance.Result, error) {
		fixture.runs <- plannedAt
		select {
		case runErr := <-fixture.runErrors:
			return auditmaintenance.Result{}, runErr
		default:
			return auditmaintenance.Result{}, nil
		}
	}
	writeNextAttempt := server.maintenanceWriteNextAttempt
	server.maintenanceWriteNextAttempt = func(
		ctx context.Context,
		path string,
		nextAttempt time.Time,
	) error {
		err := writeNextAttempt(ctx, path, nextAttempt)
		fixture.writes <- nextAttempt
		return err
	}
	return server
}

func writeMaintenanceConfig(
	t *testing.T,
	path string,
	databasePath string,
	interval time.Duration,
) {
	t.Helper()
	writeConfig(t, path, fmt.Sprintf(`
[audit]
enabled = false

[audit.storage]
maintenance_interval = %q

[audit.outputs.sqlite]
path = %q

[[rules]]
name = "block-alpha"
codex_events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "alpha"
action = "block"
violation_message = "alpha blocked"
`, interval.String(), databasePath))
}

func insertOverdueMaintenanceRun(t *testing.T, fixture *maintenanceSchedulerFixture) {
	t.Helper()
	snapshot := fixture.server.runtime.Load()
	database := snapshot.intakeStore.(*sqliteIntakeStore).Handle()
	completedAt := fixture.now.Add(-3 * time.Hour).Format(time.RFC3339Nano)
	dueAt := fixture.now.Add(-time.Hour).Format(time.RFC3339Nano)
	if _, err := database.ExecContext(t.Context(), `
		insert into audit_maintenance_runs (
			run_id, planned_at, started_at, completed_at, policy_hash,
			plan_json, result, next_due_at
		) values ('overdue', ?, ?, ?, 'hash', '{}', 'success', ?)
	`, completedAt, completedAt, completedAt, dueAt); err != nil {
		t.Fatalf("insert overdue maintenance run: %v", err)
	}
}

func readMaintenanceStatus(
	t *testing.T,
	fixture *maintenanceSchedulerFixture,
) auditmaintenance.Status {
	t.Helper()
	status, err := auditmaintenance.ReadStatus(
		t.Context(),
		fixture.databasePath,
		fixture.server.runtime.Load().cfg.AuditStoragePolicy(),
		fixture.now,
	)
	if err != nil {
		t.Fatalf("ReadStatus: %v", err)
	}
	return status
}

func receiveMaintenanceTimer(
	t *testing.T,
	timers <-chan *controlledMaintenanceTimer,
) *controlledMaintenanceTimer {
	t.Helper()
	select {
	case timer := <-timers:
		return timer
	case <-t.Context().Done():
		t.Fatal("maintenance timer was not created")
		return nil
	}
}

func receiveMaintenanceTime(t *testing.T, values <-chan time.Time) time.Time {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-t.Context().Done():
		t.Fatal("maintenance event was not received")
		return time.Time{}
	}
}

func assertNoMaintenanceRun(t *testing.T, runs <-chan time.Time) {
	t.Helper()
	select {
	case plannedAt := <-runs:
		t.Fatalf("maintenance ran before full interval at %s", plannedAt)
	default:
	}
}

func assertNoMaintenanceTimer(t *testing.T, timers <-chan *controlledMaintenanceTimer) {
	t.Helper()
	select {
	case timer := <-timers:
		t.Fatalf("maintenance timer created unexpectedly for %s", timer.duration)
	default:
	}
}

func assertNoNextAttempt(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRowContext(
		t.Context(),
		`select count(*) from audit_maintenance_schedule`,
	).Scan(&count); err != nil {
		t.Fatalf("count next attempts: %v", err)
	}
	if count != 0 {
		t.Fatalf("next attempt rows = %d, want 0", count)
	}
}

func assertNextAttempt(t *testing.T, path string, want time.Time) {
	t.Helper()
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var raw string
	if err := database.QueryRowContext(
		t.Context(),
		`select next_attempt_at from audit_maintenance_schedule where singleton = 1`,
	).Scan(&raw); err != nil {
		t.Fatalf("read next attempt: %v", err)
	}
	got, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		t.Fatalf("parse next attempt: %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("next attempt = %s, want %s", got, want)
	}
}
