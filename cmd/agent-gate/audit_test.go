package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/config"
	installer "goodkind.io/agent-gate/internal/install"
	"goodkind.io/agent-gate/internal/intake"
)

type auditFileSnapshot struct {
	exists bool
	digest string
}

func TestRunAuditStatusJSONUsesReadOnlyDatabase(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_interval = \"1ns\"\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--json"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	var status map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatalf("decode status JSON: %v", err)
	}
	for _, field := range []string{"policy", "database_bytes", "integrity_ok", "overdue"} {
		if _, ok := status[field]; !ok {
			t.Fatalf("status JSON missing %q: %s", field, stdout.String())
		}
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite files changed during status: before %#v after %#v", before, after)
	}
}

func TestRunAuditStatusCheckReportsOverdueWithoutMaintenance(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_interval = \"1ns\"\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--check"}, &stdout, &stderr)

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1 for overdue maintenance", exitCode)
	}
	if !strings.Contains(stdout.String(), "maintenance overdue") {
		t.Fatalf("stdout = %q, want overdue condition", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite files changed during check: before %#v after %#v", before, after)
	}
}

func TestRunAuditMaintainDryRunDoesNotWriteDatabase(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"maintain", "--dry-run"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "planned at:") ||
		!strings.Contains(stdout.String(), "estimated delete bytes:") {
		t.Fatalf("stdout = %q, want maintenance preview", stdout.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("SQLite files changed during dry run: before %#v after %#v", before, after)
	}
}

func TestRunAuditCompactDryRunReportsPlanWithoutWriting(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_batch_rows = 17\n")
	createAuditCommandDatabase(t)
	createAuditCommandFreePages(t, 1<<20)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--dry-run"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "pages to reclaim: 17") ||
		!strings.Contains(stdout.String(), "full compaction needed: no") {
		t.Fatalf("stdout = %q, want bounded incremental compact plan", stdout.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("compact dry-run changed SQLite files: before %#v after %#v", before, after)
	}
}

func TestFullCompactDryRunDoesNotWriteDatabase(t *testing.T) {
	stubFullCompactServiceStatus(t)
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--dry-run"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	for _, text := range []string{
		"database path:",
		"required free bytes:",
		"managed: true",
		"integrity: ok",
		"operator-controlled offline interval",
	} {
		if !strings.Contains(stdout.String(), text) {
			t.Fatalf("stdout = %q, want %q", stdout.String(), text)
		}
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("full dry-run changed SQLite files: before %#v after %#v", before, after)
	}
}

func TestFullCompactDryRunCancelsServiceInspection(t *testing.T) {
	stubCanceledAuditCommandContext(t)
	originalInspect := auditInspectService
	auditInspectService = func(
		ctx context.Context,
		_ installer.ServiceStatusOptions,
	) (installer.ServiceState, error) {
		return installer.ServiceState{}, ctx.Err()
	}
	t.Cleanup(func() {
		auditInspectService = originalInspect
	})
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--dry-run"}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stderr.String(), "service status: context canceled") {
		t.Fatalf("exit code/stderr = %d/%q, want canceled service status", exitCode, stderr.String())
	}
}

func TestFullCompactDryRunCancelsIntegrityPreflight(t *testing.T) {
	stubCanceledAuditCommandContext(t)
	stubFullCompactServiceStatus(t)
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--dry-run"}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stderr.String(), "full preflight: start full audit compaction preflight: context canceled") {
		t.Fatalf("exit code/stderr = %d/%q, want canceled full preflight", exitCode, stderr.String())
	}
}

func stubCanceledAuditCommandContext(t *testing.T) {
	t.Helper()
	originalContext := auditCommandContext
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	auditCommandContext = func() (context.Context, context.CancelFunc) {
		return ctx, func() {}
	}
	t.Cleanup(func() {
		auditCommandContext = originalContext
	})
}

func stubFullCompactServiceStatus(t *testing.T) {
	t.Helper()
	originalInspect := auditInspectService
	auditInspectService = func(
		context.Context,
		installer.ServiceStatusOptions,
	) (installer.ServiceState, error) {
		return installer.ServiceState{
			Platform: "launchd", Managed: true, Running: true, BinaryPath: "/opt/agent-gate",
		}, nil
	}
	t.Cleanup(func() {
		auditInspectService = originalInspect
	})
}

func TestFullCompactApplyPublicCLICompactsAfterExactConfirmation(t *testing.T) {
	stubStoppedFullCompactServiceStatus(t)
	stubInteractiveFullCompactInput(t, "compact audit.db\n")
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	createAuditCommandFreePages(t, 1<<20)
	before, err := os.Stat(config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--apply"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit/stderr = %d/%q", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "hooks fail open") ||
		!strings.Contains(stdout.String(), "reclaimed bytes:") {
		t.Fatalf("stdout/stderr = %q/%q", stdout.String(), stderr.String())
	}
	after, err := os.Stat(config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() >= before.Size() {
		t.Fatalf("database size = %d, want less than %d", after.Size(), before.Size())
	}
}

func TestFullCompactApplyPublicCLIRejectsConfirmationWithSurroundingSpaces(t *testing.T) {
	stubStoppedFullCompactServiceStatus(t)
	stubInteractiveFullCompactInput(t, " compact audit.db \n")
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--apply"}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stderr.String(), "confirmation did not match") {
		t.Fatalf("exit/stderr = %d/%q, want exact confirmation rejection", exitCode, stderr.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("inexact confirmation changed files: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactApplyPublicCLIRejectsNonInteractiveInputWithoutMutation(t *testing.T) {
	stubStoppedFullCompactServiceStatus(t)
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--apply"}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stderr.String(), "interactive terminal") {
		t.Fatalf("exit/stderr = %d/%q, want terminal rejection", exitCode, stderr.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("noninteractive rejection changed files: before=%#v after=%#v", before, after)
	}
}

func TestFullCompactApplyPublicCLIRejectsActiveDaemonBeforeMutation(t *testing.T) {
	stubFullCompactServiceStatus(t)
	stubInteractiveFullCompactInput(t, "compact audit.db\n")
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--apply"}, &stdout, &stderr)

	if exitCode != 1 || !strings.Contains(stderr.String(), "daemon is running") {
		t.Fatalf("exit/stderr = %d/%q, want running daemon", exitCode, stderr.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("active daemon rejection changed files: before=%#v after=%#v", before, after)
	}
}

func TestPublicWriterCommandsRejectUnresolvedCutover(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	path := config.DefaultAuditSQLitePath()
	if err := auditstorage.WriteCutoverJournal(auditstorage.CutoverJournal{
		DatabasePath: path, RunID: "interrupted",
		Phase: auditstorage.CutoverOriginalRenamed,
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshotAuditFiles(t, path)
	for _, args := range [][]string{
		{"maintain", "--apply"},
		{"compact", "--apply"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		exitCode := runAudit(args, &stdout, &stderr)
		if exitCode != 1 || !strings.Contains(stderr.String(), "recovery is required") {
			t.Fatalf("runAudit(%v) exit/stderr = %d/%q", args, exitCode, stderr.String())
		}
	}
	after := snapshotAuditFiles(t, path)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("guarded writer changed SQLite files: before=%#v after=%#v", before, after)
	}
}

func stubStoppedFullCompactServiceStatus(t *testing.T) {
	t.Helper()
	originalInspect := auditInspectService
	auditInspectService = func(
		context.Context,
		installer.ServiceStatusOptions,
	) (installer.ServiceState, error) {
		return installer.ServiceState{
			Platform: "launchd", Managed: true, Running: false, BinaryPath: "/opt/agent-gate",
		}, nil
	}
	t.Cleanup(func() { auditInspectService = originalInspect })
}

func stubInteractiveFullCompactInput(t *testing.T, input string) {
	t.Helper()
	originalInput := auditInput
	originalInteractive := auditInputIsTerminal
	auditInput = strings.NewReader(input)
	auditInputIsTerminal = func() bool { return true }
	t.Cleanup(func() {
		auditInput = originalInput
		auditInputIsTerminal = originalInteractive
	})
}

func TestFullCompactPublicCLILeavesActiveWALByteIdentical(t *testing.T) {
	stubFullCompactServiceStatus(t)
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	store := createActiveAuditCommandDatabase(t, []byte(`{"command":"make check"}`))
	defer func() { _ = store.Close() }()
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--full", "--dry-run"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "running: true") {
		t.Fatalf("exit/stdout/stderr = %d/%q/%q", exitCode, stdout.String(), stderr.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("public full dry-run changed active SQLite files: before %#v after %#v", before, after)
	}
}

func TestRunAuditCompactApplyRecordsMeasuredBytes(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_batch_rows = 1000\n")
	createAuditCommandDatabase(t)
	createAuditCommandFreePages(t, 1<<20)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--apply"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "result: success") ||
		!strings.Contains(stdout.String(), "reclaimed bytes:") {
		t.Fatalf("stdout = %q, want compact result", stdout.String())
	}
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open compacted database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var reclaimed int64
	if err := database.QueryRowContext(t.Context(), `
		select reclaimed_bytes from audit_maintenance_runs order by started_at desc limit 1
	`).Scan(&reclaimed); err != nil {
		t.Fatalf("read compact run: %v", err)
	}
	if reclaimed <= 0 {
		t.Fatalf("reclaimed bytes = %d, want positive", reclaimed)
	}
}

func TestRunAuditCompactApplyDefersForReaderPinnedLiveWAL(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_batch_rows = 1000\n")
	createAuditCommandDatabase(t)
	createAuditCommandFreePages(t, 1<<20)
	path := config.DefaultAuditSQLitePath()
	reader, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open reader: %v", err)
	}
	defer func() { _ = reader.Close() }()
	readerConnection, err := reader.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve reader: %v", err)
	}
	defer func() { _ = readerConnection.Close() }()
	if _, err := readerConnection.ExecContext(t.Context(), `begin`); err != nil {
		t.Fatalf("begin reader: %v", err)
	}
	readerActive := true
	defer func() {
		if readerActive {
			_, _ = readerConnection.ExecContext(t.Context(), `rollback`)
		}
	}()
	var rows int
	if err := readerConnection.QueryRowContext(
		t.Context(), `select count(*) from intake_events`,
	).Scan(&rows); err != nil {
		t.Fatalf("establish reader snapshot: %v", err)
	}
	writer, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()
	if _, err := writer.ExecContext(t.Context(), `
		update intake_events set command = command || 'x'
		where event_id = 'audit-command-event'
	`); err != nil {
		t.Fatalf("write live WAL frame: %v", err)
	}
	var beforeFree int64
	if err := writer.QueryRowContext(t.Context(), `pragma freelist_count`).Scan(&beforeFree); err != nil {
		t.Fatalf("read free pages before compact: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"compact", "--apply"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "maintenance deferred:") ||
		!strings.Contains(stdout.String(), "result: deferred") ||
		!strings.Contains(stdout.String(), "reclaimed bytes: 0") {
		t.Fatalf("stdout = %q, want deferred compact without reclaimed bytes", stdout.String())
	}
	var afterFree int64
	if err := writer.QueryRowContext(t.Context(), `pragma freelist_count`).Scan(&afterFree); err != nil {
		t.Fatalf("read free pages after compact: %v", err)
	}
	if afterFree != beforeFree {
		t.Fatalf("free pages changed from %d to %d during deferred compact", beforeFree, afterFree)
	}
	if _, err := readerConnection.ExecContext(t.Context(), `rollback`); err != nil {
		t.Fatalf("release reader: %v", err)
	}
	readerActive = false
}

func TestRunAuditCompactRequiresExactlyOneMode(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	for _, args := range [][]string{
		{"compact"},
		{"compact", "--full"},
		{"compact", "--dry-run", "--apply"},
		{"compact", "--full", "--dry-run", "--apply"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if exitCode := runAudit(args, &stdout, &stderr); exitCode != 2 {
			t.Fatalf("runAudit(%v) exit code = %d, want 2", args, exitCode)
		}
	}
}

func TestRunAuditMaintainDryRunPredictsSizeDrivenApplyAndStoredPlan(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
full_detail_retention = "200000h"
summary_retention = "200000h"
max_size_mb = 1
`)
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "recent-large-eligible", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     bytes.Repeat([]byte("x"), 2_000_000),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append eligible graph: %v", err)
	}
	insertCommandCompletedEvaluation(t, store.Handle(), receipt.ReceiptID, receipt.EventID)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	var dryRunOutput bytes.Buffer
	var stderr bytes.Buffer

	if exitCode := runAudit([]string{"maintain", "--dry-run"}, &dryRunOutput, &stderr); exitCode != 0 {
		t.Fatalf("dry-run exit/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(dryRunOutput.String(), "summary candidate graphs: 1") {
		t.Fatalf("dry-run stdout = %q, want one size candidate", dryRunOutput.String())
	}
	after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("dry-run changed SQLite files: before %#v after %#v", before, after)
	}
	var applyOutput bytes.Buffer
	stderr.Reset()
	if exitCode := runAudit([]string{"maintain", "--apply"}, &applyOutput, &stderr); exitCode != 0 {
		t.Fatalf("apply exit/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(applyOutput.String(), "summary graphs: 1") {
		t.Fatalf("apply stdout = %q, want one deleted graph", applyOutput.String())
	}
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open maintained database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var planJSON string
	if err := database.QueryRowContext(t.Context(), `
		select plan_json from audit_maintenance_runs order by started_at desc limit 1
	`).Scan(&planJSON); err != nil {
		t.Fatalf("read stored plan: %v", err)
	}
	var plan struct {
		SummaryCandidateGraphs int64 `json:"summary_candidate_graphs"`
	}
	if err := json.Unmarshal([]byte(planJSON), &plan); err != nil {
		t.Fatalf("decode stored plan: %v", err)
	}
	if plan.SummaryCandidateGraphs != 1 {
		t.Fatalf("stored summary candidates = %d, want 1", plan.SummaryCandidateGraphs)
	}
}

func TestRunAuditMaintainApplyDeletesEligibleGraphs(t *testing.T) {
	setupAuditCommandEnvironment(t, `[audit.storage]
full_detail_retention = "1h"
summary_retention = "2h"
`)
	createCompletedAuditCommandDatabase(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"maintain", "--apply"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "result: success") ||
		!strings.Contains(stdout.String(), "next due at:") ||
		!strings.Contains(stdout.String(), "size state: disabled") {
		t.Fatalf("stdout = %q, want successful result, size state, and next due time", stdout.String())
	}
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open maintained database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRow(`select count(*) from intake_events`).Scan(&count); err != nil {
		t.Fatalf("count maintained events: %v", err)
	}
	if count != 0 {
		t.Fatalf("maintained events = %d, want 0", count)
	}
}

func TestAuditMaintainApplyAcceptsQuarantinedLegacyEvaluations(t *testing.T) {
	t.Skip("database backward compatibility was removed")
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	installAuditCommandLegacyOrphanFixture(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"maintain", "--apply"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "result: success") {
		t.Fatalf("stdout = %q, want successful maintenance", stdout.String())
	}
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open maintained legacy database: %v", err)
	}
	defer func() { _ = database.Close() }()
	var count int
	if err := database.QueryRowContext(t.Context(), `
		select count(*) from audit_migration_quarantined_evaluations
	`).Scan(&count); err != nil {
		t.Fatalf("count maintained legacy quarantine: %v", err)
	}
	if count != 20 {
		t.Fatalf("maintained quarantined evaluations = %d, want 20", count)
	}
	rows, err := database.QueryContext(t.Context(), `pragma foreign_key_check`)
	if err != nil {
		t.Fatalf("foreign_key_check: %v", err)
	}
	defer func() { _ = rows.Close() }()
	if rows.Next() {
		t.Fatal("foreign_key_check returned a violation after maintenance")
	}
}

func TestRunAuditMaintainApplyDefersDuringSchemaMigrationContention(t *testing.T) {
	t.Skip("database backward compatibility was removed")
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	blocker, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open migration blocker: %v", err)
	}
	defer func() { _ = blocker.Close() }()
	if _, err := blocker.ExecContext(t.Context(), `
		drop table audit_maintenance_schedule;
		drop table audit_maintenance_runs;
		drop table audit_maintenance_lease;
		delete from audit_schema_migrations where version >= 5;
		pragma user_version = 4;
	`); err != nil {
		t.Fatalf("restore version four schema: %v", err)
	}
	connection, err := blocker.Conn(t.Context())
	if err != nil {
		t.Fatalf("reserve migration blocker: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.ExecContext(t.Context(), `begin immediate`); err != nil {
		t.Fatalf("begin migration blocker: %v", err)
	}
	defer func() { _, _ = connection.ExecContext(context.Background(), `rollback`) }()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	startedAt := time.Now()
	exitCode := runAudit([]string{"maintain", "--apply"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "maintenance deferred:") {
		t.Fatalf("stdout = %q, want deferred maintenance", stdout.String())
	}
	if strings.Contains(stdout.String(), "run id:") {
		t.Fatalf("stdout = %q, want no empty run identifier", stdout.String())
	}
	if elapsed := time.Since(startedAt); elapsed >= time.Second {
		t.Fatalf("maintenance deferred after %s, want less than one second", elapsed)
	}
}

func TestRunAuditMaintainRequiresExactlyOneMode(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	for _, args := range [][]string{
		{"maintain"},
		{"maintain", "--dry-run", "--apply"},
	} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if exitCode := runAudit(args, &stdout, &stderr); exitCode != 2 {
			t.Fatalf("runAudit(%v) exit code = %d, want 2", args, exitCode)
		}
	}
}

func TestReadOnlyAuditCommandsSeeActiveWALWithoutWritingSource(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\nmaintenance_interval = \"200000h\"\n")
	store := createActiveAuditCommandDatabase(t, []byte(`{"command":"make check"}`))
	defer func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close active audit store: %v", err)
		}
	}()
	commands := [][]string{
		{"status"},
		{"status", "--json"},
		{"status", "--check"},
		{"maintain", "--dry-run"},
	}
	for _, command := range commands {
		before := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
		var stdout bytes.Buffer
		var stderr bytes.Buffer

		exitCode := runAudit(command, &stdout, &stderr)

		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("command %v exit/stderr = %d/%q, want 0/empty", command, exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "protected") {
			t.Fatalf("command %v stdout = %q, want committed WAL graph", command, stdout.String())
		}
		after := snapshotAuditFiles(t, config.DefaultAuditSQLitePath())
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("command %v changed SQLite files: before %#v after %#v", command, before, after)
		}
	}
}

func TestRunAuditStatusCheckAcceptsProtectedConstraint(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
max_size_mb = 1
`)
	store := createActiveAuditCommandDatabase(t, bytes.Repeat([]byte("x"), 2_000_000))
	if err := store.Close(); err != nil {
		t.Fatalf("Close constrained audit store: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--check"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "size state: constrained") {
		t.Fatalf("stdout = %q, want constrained size state", stdout.String())
	}
}

func TestRunAuditStatusReportsProtectedBytesInPlainAndJSON(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
max_size_mb = 1
`)
	store := createActiveAuditCommandDatabase(t, bytes.Repeat([]byte("x"), 2_000_000))
	if err := store.Close(); err != nil {
		t.Fatalf("Close constrained audit store: %v", err)
	}
	var plain bytes.Buffer
	var stderr bytes.Buffer
	if exitCode := runAudit([]string{"status"}, &plain, &stderr); exitCode != 0 {
		t.Fatalf("plain status exit/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(plain.String(), "protected bytes estimate:") ||
		strings.Contains(plain.String(), "protected bytes estimate: 0\n") {
		t.Fatalf("plain status = %q, want nonzero protected bytes estimate", plain.String())
	}
	var jsonOutput bytes.Buffer
	stderr.Reset()
	if exitCode := runAudit([]string{"status", "--json"}, &jsonOutput, &stderr); exitCode != 0 {
		t.Fatalf("JSON status exit/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	var status struct {
		ProtectedBytes int64  `json:"protected_bytes"`
		SizeState      string `json:"size_state"`
	}
	if err := json.Unmarshal(jsonOutput.Bytes(), &status); err != nil {
		t.Fatalf("decode status JSON: %v", err)
	}
	if status.ProtectedBytes == 0 || status.SizeState != "constrained" {
		t.Fatalf("JSON status = %#v, want protected bytes and constrained", status)
	}
}

func TestRunAuditStatusCheckFailsCurrentUnconstrainedTarget(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
max_size_mb = 1
`)
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "large-eligible", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload:     bytes.Repeat([]byte("x"), 2_000_000),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append eligible graph: %v", err)
	}
	insertCommandCompletedEvaluation(t, store.Handle(), receipt.ReceiptID, receipt.EventID)
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--check"}, &stdout, &stderr)

	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 1/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "size state: over_target") {
		t.Fatalf("stdout = %q, want over_target size state", stdout.String())
	}
}

func TestRunAuditStatusCheckAcceptsMixedProtectedConstraint(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
max_size_mb = 1
`)
	store := createActiveAuditCommandDatabase(t, bytes.Repeat([]byte("p"), 950_000))
	eligible, err := store.Append(t.Context(), intake.Record{
		EventID: "tiny-command-eligible", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: []byte(`{}`), NormalizedJSON: json.RawMessage(`{}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append eligible graph: %v", err)
	}
	insertCommandCompletedEvaluation(t, store.Handle(), eligible.ReceiptID, eligible.EventID)
	if err := store.Close(); err != nil {
		t.Fatalf("Close mixed command store: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--check"}, &stdout, &stderr)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "size state: constrained") {
		t.Fatalf("stdout = %q, want constrained size state", stdout.String())
	}
}

func TestRunAuditStatusCheckFailsReclaimPending(t *testing.T) {
	setupAuditCommandEnvironment(t, `
[audit.storage]
maintenance_interval = "200000h"
max_size_mb = 1
`)
	createAuditCommandDatabase(t)
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		create table free_padding (value blob not null);
		insert into free_padding values (zeroblob(2097152));
		drop table free_padding;
	`); err != nil {
		_ = database.Close()
		t.Fatalf("create free allocation: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close audit database: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := runAudit([]string{"status", "--check"}, &stdout, &stderr)

	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 1/empty", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "size state: reclaim_pending") {
		t.Fatalf("stdout = %q, want reclaim_pending size state", stdout.String())
	}
}

func TestRunCLIRoutesAuditWithoutHookMode(t *testing.T) {
	setupAuditCommandEnvironment(t, "[audit.storage]\n")
	createAuditCommandDatabase(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	hookCalls := 0

	exitCode := runCLIWithHook(
		[]string{"audit", "status", "--json"},
		&stdout,
		&stderr,
		func(hookRoute) int {
			hookCalls++
			return 0
		},
	)

	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("exit code/stderr = %d/%q, want 0/empty", exitCode, stderr.String())
	}
	if hookCalls != 0 {
		t.Fatalf("hook calls = %d, want 0", hookCalls)
	}
	if !strings.Contains(stdout.String(), `"database_bytes"`) {
		t.Fatalf("stdout = %q, want audit status JSON", stdout.String())
	}
}

func setupAuditCommandEnvironment(t *testing.T, configBody string) {
	t.Helper()
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(directory, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(directory, "runtime"))
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0o700); err != nil {
		t.Fatalf("MkdirAll config: %v", err)
	}
	if err := os.WriteFile(config.Path(), []byte(configBody), 0o600); err != nil {
		t.Fatalf("WriteFile config: %v", err)
	}
}

func createAuditCommandDatabase(t *testing.T) {
	t.Helper()
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "audit-command-event", RecordedAt: time.Now().Add(-60 * 24 * time.Hour),
		System: "codex", SessionID: "session", EventName: "PreToolUse",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("Append: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		_ = store.Close()
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func createAuditCommandFreePages(t *testing.T, paddingBytes int) {
	t.Helper()
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open audit database: %v", err)
	}
	if _, err := database.ExecContext(t.Context(), `
		pragma wal_checkpoint(truncate);
		create table compact_padding(value blob not null);
		insert into compact_padding values (zeroblob(?));
		pragma wal_checkpoint(truncate);
		drop table compact_padding;
		pragma wal_checkpoint(truncate);
	`, paddingBytes); err != nil {
		_ = database.Close()
		t.Fatalf("create compact free pages: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close audit database: %v", err)
	}
}

func installAuditCommandLegacyOrphanFixture(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(config.DefaultAuditSQLitePath()), 0o700); err != nil {
		t.Fatalf("create legacy audit command directory: %v", err)
	}
	database, err := sql.Open("sqlite3", config.DefaultAuditSQLitePath())
	if err != nil {
		t.Fatalf("open legacy audit command database: %v", err)
	}
	for _, name := range []string{"legacy_v1.sql", "legacy_orphan_evaluations.sql"} {
		fixture, err := os.ReadFile(filepath.Join("..", "..", "internal", "auditstorage", "testdata", name))
		if err != nil {
			_ = database.Close()
			t.Fatalf("read %s: %v", name, err)
		}
		if _, err := database.ExecContext(t.Context(), string(fixture)); err != nil {
			_ = database.Close()
			t.Fatalf("install %s: %v", name, err)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close legacy audit command database: %v", err)
	}
}

func createCompletedAuditCommandDatabase(t *testing.T) {
	t.Helper()
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	receipt, err := store.Append(t.Context(), intake.Record{
		EventID: "audit-command-complete", RecordedAt: time.Now().Add(-24 * time.Hour),
		System: "codex", SessionID: "session", EventName: "PreToolUse",
		RawPayload:     []byte(`{"command":"make check"}`),
		NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("Append: %v", err)
	}
	insertCommandCompletedEvaluation(t, store.Handle(), receipt.ReceiptID, receipt.EventID)
	if _, err := store.Handle().ExecContext(
		t.Context(),
		`update intake_receipts set received_at = ? where receipt_id = ?`,
		time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339Nano),
		receipt.ReceiptID,
	); err != nil {
		_ = store.Close()
		t.Fatalf("age command receipt: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func createActiveAuditCommandDatabase(t *testing.T, payload []byte) *intake.Store {
	t.Helper()
	store, err := intake.OpenSQLite(t.Context(), config.DefaultAuditSQLitePath(), nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_autocheckpoint = 0`); err != nil {
		_ = store.Close()
		t.Fatalf("disable WAL auto-checkpoint: %v", err)
	}
	if _, err := store.Handle().ExecContext(t.Context(), `pragma wal_checkpoint(truncate)`); err != nil {
		_ = store.Close()
		t.Fatalf("checkpoint active command database: %v", err)
	}
	if _, err := store.Append(t.Context(), intake.Record{
		EventID: "active-command-event", RecordedAt: time.Now().UTC(), System: "codex",
		SessionID: "session", EventName: "PreToolUse", ToolName: "exec_command",
		RawPayload: payload, NormalizedJSON: json.RawMessage(`{"command":"make check"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatalf("Append active audit graph: %v", err)
	}
	return store
}

func insertCommandCompletedEvaluation(
	t *testing.T,
	database *sql.DB,
	receiptID int64,
	eventID string,
) {
	t.Helper()
	if _, err := database.ExecContext(t.Context(), `
		insert into gate_evaluations (
			evaluation_id, receipt_id, event_id, attempt, mode, config_hash,
			engine_version, engine_commit, engine_build_hash, input_hash,
			started_at, completed_at, final_verdict, final_source,
			enforcement_action, enforced, total_latency_us, layer_count,
			label_count, detail_state
		) values (?, ?, ?, 1, 'hot', 'config', 'version', 'commit', 'build',
			'input', '2030-08-01T00:00:00Z', '2030-08-01T00:00:01Z',
			'allow', 'deterministic', 'allow', 0, 1, 0, 0, 'available')
	`, "evaluation-"+eventID, receiptID, eventID); err != nil {
		t.Fatalf("insert command evaluation: %v", err)
	}
}

func snapshotAuditFiles(t *testing.T, path string) map[string]auditFileSnapshot {
	t.Helper()
	result := make(map[string]auditFileSnapshot)
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		body, err := os.ReadFile(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				result[candidate] = auditFileSnapshot{exists: false, digest: ""}
				continue
			}
			t.Fatalf("ReadFile %s: %v", candidate, err)
		}
		digest := sha256.Sum256(body)
		result[candidate] = auditFileSnapshot{
			exists: true,
			digest: hex.EncodeToString(digest[:]),
		}
	}
	return result
}
