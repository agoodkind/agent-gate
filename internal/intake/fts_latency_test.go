package intake_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"goodkind.io/agent-gate/internal/intake"
)

const intakeEventsSchema = `create table intake_events (
	seq integer primary key autoincrement,
	event_id text not null unique,
	schema_version integer not null,
	recorded_at text not null,
	system text not null,
	session_id text not null,
	turn_id text not null,
	event_name text not null,
	tool_name text not null,
	tool_use_id text not null,
	cwd text not null,
	effective_cwd text not null,
	command text not null,
	file_path text not null,
	raw_payload blob not null,
	raw_payload_hash text not null,
	normalized_json text not null,
	env_fingerprint_json text not null default '{}'
)`

// TestCommandFTSBackfillsPreexistingRows reproduces the production path where a
// populated intake DB gains the FTS index for the first time: the rows exist
// before the FTS table is created, so the one-time rebuild (not the triggers)
// must populate the index. This guards against the external-content count(*)
// pitfall where the rebuild is skipped because count(*) reads the content table.
func TestCommandFTSBackfillsPreexistingRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "preexisting.db")

	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.ExecContext(ctx, intakeEventsSchema); err != nil {
		t.Fatalf("create intake_events: %v", err)
	}
	const total = 60
	anthropic := 0
	for i := 1; i <= total; i++ {
		command := fmt.Sprintf("grep -rn token file%d.go", i)
		if i%2 == 0 {
			command = fmt.Sprintf("grep -rn anthropic api%d.go", i)
			anthropic++
		}
		_, err := raw.ExecContext(ctx, `insert into intake_events (
			event_id, schema_version, recorded_at, system, session_id, turn_id,
			event_name, tool_name, tool_use_id, cwd, effective_cwd, command,
			file_path, raw_payload, raw_payload_hash, normalized_json
		) values (?, 1, '2026-06-01T00:00:00Z', 'claude', 's', 't', 'PreToolUse', 'Bash', ?, '', '', ?, '', ?, 'h', '{}')`,
			fmt.Sprintf("evt-%d", i), fmt.Sprintf("tu-%d", i), command, []byte("{}"))
		if err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	// Opening the store runs the schema bootstrap, which creates the FTS index
	// and backfills the pre-existing rows.
	store, err := intake.OpenSQLite(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("reopen raw: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	var matched int
	if err := db.QueryRowContext(ctx, `select count(*) from intake_command_fts where command match 'anthropic'`).Scan(&matched); err != nil {
		t.Fatalf("fts match query: %v", err)
	}
	if matched != anthropic {
		t.Fatalf("rebuild should index all pre-existing rows: matched %d, want %d", matched, anthropic)
	}
}

func TestHotEvalLatencyColumnAndCommandFTS(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.db")
	store, err := intake.OpenSQLite(ctx, path, nil)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	defer func() {
		_ = store.Close()
	}()

	appended, err := store.Append(ctx, intake.Record{
		System:         "claude",
		SessionID:      "session-1",
		TurnID:         "turn-1",
		EventName:      "PreToolUse",
		ToolName:       "Bash",
		ToolUseID:      "toolu_1",
		Operation:      intake.Operation{Command: "grep -rn anthropic ./internal --include=*.go"},
		RawPayload:     []byte(`{"event":"pre"}`),
		NormalizedJSON: []byte(`{"provider":"claude"}`),
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := store.UpdateHotEvalLatency(ctx, appended.EventID, 12345); err != nil {
		t.Fatalf("UpdateHotEvalLatency: %v", err)
	}

	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() {
		_ = db.Close()
	}()

	var latency sql.NullInt64
	if err := db.QueryRowContext(ctx, `select hot_eval_latency_us from intake_events where event_id = ?`, appended.EventID).Scan(&latency); err != nil {
		t.Fatalf("read latency: %v", err)
	}
	if !latency.Valid || latency.Int64 != 12345 {
		t.Fatalf("expected latency 12345, got %+v", latency)
	}

	var p95 sql.NullInt64
	if err := db.QueryRowContext(ctx, `select hot_eval_latency_us from intake_events where hot_eval_latency_us is not null order by hot_eval_latency_us limit 1`).Scan(&p95); err != nil {
		t.Fatalf("percentile query: %v", err)
	}
	if !p95.Valid {
		t.Fatalf("expected a non-null latency percentile sample")
	}

	var matched int
	if err := db.QueryRowContext(ctx, `
		select count(*) from intake_command_fts f
		join intake_events e on e.seq = f.rowid
		where f.command like '%anthropic%'
	`).Scan(&matched); err != nil {
		t.Fatalf("fts query: %v", err)
	}
	if matched != 1 {
		t.Fatalf("expected FTS substring match to find the command, got %d", matched)
	}

	// A latency-only update must not churn the FTS index (trigger scoped to the
	// command column), so the FTS row count stays consistent.
	if err := store.UpdateHotEvalLatency(ctx, appended.EventID, 999); err != nil {
		t.Fatalf("second UpdateHotEvalLatency: %v", err)
	}
	var ftsRows int
	if err := db.QueryRowContext(ctx, `select count(*) from intake_command_fts`).Scan(&ftsRows); err != nil {
		t.Fatalf("count fts rows: %v", err)
	}
	if ftsRows != 1 {
		t.Fatalf("expected exactly one FTS row after a latency-only update, got %d", ftsRows)
	}
}
