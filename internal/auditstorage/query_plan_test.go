package auditstorage_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/auditstorage"
	"goodkind.io/agent-gate/internal/evaluation"
	"goodkind.io/agent-gate/internal/intake"
)

func TestSurvivingQueryPlans(t *testing.T) {
	store, err := openFixtureIntake(t, t.Context(), filepath.Join(t.TempDir(), "audit.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Handle().Close() })
	output := json.RawMessage(`{}`)
	digest := sha256.Sum256(output)
	events := make([]audit.Event, 0, 200)
	for index := range 200 {
		id := fmt.Sprintf("event-%03d", index)
		at := time.Date(2026, 9, 8, 0, index, 0, 0, time.UTC)
		receipt, err := store.Append(t.Context(), intake.Record{EventID: id, RecordedAt: at, System: "codex", SessionID: fmt.Sprintf("session-%d", index%10), EventName: "PreToolUse", ToolName: "Shell", RawPayload: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		record := evaluation.Record{
			Evaluation: evaluation.Evaluation{EvaluationID: "evaluation-" + id, ReceiptID: receipt.ReceiptID, EventID: id, Mode: "hot", Attempt: 1, StartedAt: at, CompletedAt: at, FinalVerdict: "allow", ErrorJSON: json.RawMessage(`{}`)},
			Layers:     []evaluation.Layer{{LayerIndex: 0, Kind: "deterministic", Name: "rules", Status: "complete", Outcome: "nonmatch", InputJSON: output, OutputJSON: output, OutputHash: "sha256:" + hex.EncodeToString(digest[:]), MetadataJSON: json.RawMessage(`{"schema_version":1}`), StartedAt: at, CompletedAt: at}},
			Labels:     []evaluation.Label{{Namespace: "label", LabelVersion: 1, Verdict: "allow", Source: "test", CreatedAt: at}},
		}
		if err := store.CommitHotEvaluation(t.Context(), id, receipt.ReceiptID, index%3 == 0, record, nil); err != nil {
			t.Fatal(err)
		}
		events = append(events, audit.Event{EventID: id, SchemaVersion: 1, Time: at.Format(time.RFC3339Nano), System: "codex", SessionID: "session-1", EventName: "PreToolUse", ToolName: "Shell", Decision: audit.Decision{Kind: "allow"}, Violations: []audit.Violation{{Rule: "rule"}}})
	}
	if err := audit.WriteEvents(t.Context(), store.Handle(), events); err != nil {
		t.Fatal(err)
	}
	for _, query := range strings.Split(readFixture(t, "query_plans.sql"), ";") {
		query = strings.TrimSpace(query)
		if query == "" {
			continue
		}
		rows, err := store.Handle().QueryContext(t.Context(), "explain query plan "+query)
		if err != nil {
			t.Fatal(err)
		}
		plans := make([]string, 0)
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			plans = append(plans, detail)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
		if len(plans) == 0 {
			t.Fatal("query plan is empty")
		}
		t.Logf("%s\n%s", query, strings.Join(plans, "\n"))
	}
	for _, name := range []string{"cost_1.sql", "cost_2.sql"} {
		query, err := os.ReadFile(filepath.Join("..", "evaluation", name))
		if err != nil {
			t.Fatal(err)
		}
		rows, err := store.Handle().QueryContext(t.Context(), "explain query plan "+string(query), "", "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id, parent, unused int
			var detail string
			if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			t.Logf("%s: %s", name, detail)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAuditEventChildQueryPlans(t *testing.T) {
	database, err := auditstorage.OpenWriter(t.Context(), filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	events := make([]audit.Event, 0, 200)
	for index := range 200 {
		events = append(events, audit.Event{EventID: fmt.Sprintf("child-%03d", index), Violations: []audit.Violation{{Rule: "rule"}}})
	}
	if err := audit.WriteEvents(t.Context(), database, events); err != nil {
		t.Fatal(err)
	}
	parentQuery, err := os.ReadFile(filepath.Join("..", "audit", "query_1.sql"))
	if err != nil {
		t.Fatal(err)
	}
	parentPlan := explainChildQuery(t, database, string(parentQuery)+readFixture(t, "audit_event_filter.sql"))
	for _, expected := range []string{"SEARCH o USING INDEX sqlite_autoindex_operations_1 (event_id=?)", "SEARCH d USING INDEX sqlite_autoindex_decisions_1 (event_id=?)"} {
		if !strings.Contains(parentPlan, expected) {
			t.Errorf("parent query plan lacks %q: %s", expected, parentPlan)
		}
	}
	violationQuery, err := os.ReadFile(filepath.Join("..", "audit", "query_2.sql"))
	if err != nil {
		t.Fatal(err)
	}
	violationPlan := explainChildQuery(t, database, string(violationQuery))
	if !strings.Contains(violationPlan, "SEARCH violations USING INDEX violation_event_idx (event_id=?)") || strings.Contains(violationPlan, "TEMP B-TREE") {
		t.Fatalf("violation query must use event ownership index without sorting: %s", violationPlan)
	}
}

func explainChildQuery(t *testing.T, database *sql.DB, query string) string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "explain query plan "+query, "child-100")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	plans := make([]string, 0)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	plan := strings.Join(plans, "\n")
	t.Logf("%s\n%s", query, plan)
	return plan
}
