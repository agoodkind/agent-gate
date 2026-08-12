package main

import (
	"bytes"
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

	"goodkind.io/agent-gate/internal/config"
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
