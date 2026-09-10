package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestHotAuditPersistsOriginalInferenceDecision(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		for _, eventName := range []string{"PreToolUse", "Stop"} {
			t.Run(profile+"/"+eventName, func(t *testing.T) {
				setDaemonTestDirs(t)
				fake := newDeferredInferenceFake("")
				cfg := loadHotAuditInferConfig(t, startDeferredInferenceServer(t, fake), eventName)
				server := hotAuditServer(t, cfg, profile)
				response, err := server.EvaluateHook(t.Context(), &daemonpb.EvaluateHookRequest{
					ProviderHint: "codex",
					RawJson:      []byte(`{"session_id":"hot-audit","hook_event_name":"` + eventName + `","last_assistant_message":"original hot message","tool_name":"Shell","tool_input":{"command":"echo audit"}}`),
				})
				if err != nil || response.ExitCode != 0 || len(response.StderrData) != 0 {
					t.Fatalf("response = %+v, error = %v", response, err)
				}
				wantMessages := []string{"hook.received", "hook.blocked"}
				wantDecision := "block"
				wantCanBlock := true
				if eventName == "Stop" {
					wantMessages = []string{"hook.audit_violation", "hook.allowed"}
					wantDecision = "audit_only"
					wantCanBlock = false
					if strings.TrimSpace(string(response.StdoutData)) != "{}" {
						t.Fatalf("provider downgrade response = %q", response.StdoutData)
					}
				} else if !strings.Contains(string(response.StdoutData), `"permissionDecision":"deny"`) || !strings.Contains(string(response.StdoutData), "infer-block") {
					t.Fatalf("inference block response = %q", response.StdoutData)
				}
				events := readHotAuditEvents(t, server)
				assertHotAuditMessages(t, events, wantMessages)
				var matched *audit.QueryRecord
				for index := range events {
					if events[index].Decision.Kind == wantDecision {
						matched = &events[index]
					}
				}
				if matched == nil || matched.Decision.CanBlock != wantCanBlock || !reflect.DeepEqual(matched.Decision.RulesMatched, []string{"infer-block"}) || !reflect.DeepEqual(matched.Decision.RulesChecked, []string{"infer-block"}) || len(matched.Violations) != 1 || matched.Violations[0].Rule != "infer-block" || !strings.Contains(matched.Violations[0].Message, "blocked") {
					t.Fatalf("original hot audit = %+v", matched)
				}
				if fake.callCount() != 1 {
					t.Fatalf("inference calls = %d, want 1", fake.callCount())
				}
				var hotLayers, deferredLayers int
				if err := server.runtime.Load().bucketHandle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "count_phase_inference.sql")).Scan(&hotLayers, &deferredLayers); err != nil || hotLayers != 1 || deferredLayers != 0 {
					t.Fatalf("inference layers hot/deferred = %d/%d, error = %v", hotLayers, deferredLayers, err)
				}
			})
		}
	}
}

func TestHotAuditPersistsResponseEffects(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		for _, blocked := range []bool{false, true} {
			name := "applied"
			if blocked {
				name = "suppressed_by_block"
			}
			t.Run(profile+"/"+name, func(t *testing.T) {
				setDaemonTestDirs(t)
				cfg := daemonTestConfig(t)
				if !blocked {
					cfg.Rules = nil
				}
				cfg.Rules = append(cfg.Rules, config.Rule{
					Name: "hot-context", Events: []string{"PreToolUse"},
					Action: config.ActionInject, Output: "original context",
				})
				server := hotAuditServer(t, cfg, profile)
				request := blockingLedgerRequest(t)
				response, err := server.EvaluateHook(t.Context(), request)
				if err != nil || response.ExitCode != 0 {
					t.Fatalf("response = %+v, error = %v", response, err)
				}
				if strings.Contains(string(response.StdoutData), "original context") == blocked {
					t.Fatalf("response effect suppression = %q", response.StdoutData)
				}
				events := readHotAuditEvents(t, server)
				want := []string{"hook.response_effect", "hook.allowed"}
				if blocked {
					want = []string{"hook.received", "hook.blocked", "hook.response_effect"}
				}
				assertHotAuditMessages(t, events, want)
				expected := server.runtime.Load().eventLogger.Normalize("codex", "ledger-session", "PreToolUse", "info", "hook.response_effect", audit.Attrs{
					"system": audit.NewStringValue("codex"), "event": audit.NewStringValue("PreToolUse"),
					"session_id": audit.NewStringValue("ledger-session"), "rule": audit.NewStringValue("hot-context"),
					"effect_type": audit.NewStringValue("inject"), "target": audit.NewStringValue("context"),
					"byte_count": audit.NewIntValue(int64(len("original context"))), "disposition": audit.NewStringValue(name),
				})
				for _, event := range events {
					if event.Message == "hook.response_effect" && event.EventID != expected.Event.EventID {
						t.Fatalf("effect fingerprint = %s, want %s", event.EventID, expected.Event.EventID)
					}
				}
			})
		}
	}
}

func TestHotAuditInsertFailureReturnsFailOpen(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		t.Run(profile, func(t *testing.T) {
			setDaemonTestDirs(t)
			server := hotAuditServer(t, daemonTestConfig(t), profile)
			snapshot := server.runtime.Load()
			if _, err := snapshot.bucketHandle.Database.ExecContext(t.Context(), readDaemonSQLFixture(t, "fail_hot_audit_insert.sql")); err != nil {
				t.Fatal(err)
			}
			response, err := server.EvaluateHook(t.Context(), blockingLedgerRequest(t))
			if err != nil || response.ExitCode != 0 {
				t.Fatalf("response = %+v, error = %v", response, err)
			}
			assertSaysUnevaluated(t, response, hook.FailOpenReasonVerdictNotRecorded)
			if events := readHotAuditEvents(t, server); len(events) != 0 {
				t.Fatalf("audit survived rollback: %+v", events)
			}
			pending, err := snapshot.intakeStore.ListPending(t.Context())
			if err != nil || len(pending) != 0 {
				t.Fatalf("pending = %v, error = %v", pending, err)
			}
			var verdict, action string
			var enforced bool
			if err := snapshot.bucketHandle.Database.QueryRowContext(t.Context(), readDaemonSQLFixture(t, "hot_evaluation_summary.sql")).Scan(&verdict, &action, &enforced); err != nil || verdict != "error" || action != "fail_open" || enforced {
				t.Fatalf("fallback = %s/%s/%v, error = %v", verdict, action, enforced, err)
			}
		})
	}
}

func hotAuditServer(t *testing.T, cfg *config.Config, profile string) *Server {
	t.Helper()
	enabled := true
	cfg.Audit.Enabled = &enabled
	cfg.Audit.Storage.Profile = profile
	cfg.Audit.Outputs.SQLite.Path = filepath.Join(t.TempDir(), "audit.db")
	server, err := newServer(t.Context(), newDiscardLogger(), cfg, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	// Hold deferred processing so only hot completion can account for these rows.
	server.runtime.Load().deferredProcessor.Close()
	return server
}

func loadHotAuditInferConfig(t *testing.T, endpoint string, eventName string) *config.Config {
	t.Helper()
	inputField := "tool_input.command"
	if eventName == "Stop" {
		inputField = "last_assistant_message"
	}
	body := `
[[rules]]
name = "infer-block"
events = ["` + eventName + `"]
action = "block"
violation_message = "blocked"
[[rules.conditions]]
kind = "infer"
endpoint = "` + endpoint + `"
layer_name = "classification"
prompt = "Classify"
input_field = "` + inputField + `"
output_schema = '{"type":"object"}'
response_json_field = "decision"
response_json_equals = "block"
cache_ttl_ms = 0

[[rules]]
name = "audit-echo"
events = ["PreToolUse"]
action = "audit"
violation_message = "audit"
pattern = "echo audit"
field_paths = ["tool_input.command"]
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readHotAuditEvents(t *testing.T, server *Server) []audit.QueryRecord {
	t.Helper()
	snapshot := server.runtime.Load()
	queryConfig := *snapshot.cfg
	events, _, err := audit.QueryReadOnly(t.Context(), &queryConfig, audit.QueryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func assertHotAuditMessages(t *testing.T, events []audit.QueryRecord, want []string) {
	t.Helper()
	counts := make(map[string]int)
	for _, event := range events {
		counts[event.Message]++
	}
	if len(events) != len(want) {
		t.Fatalf("audit messages = %v, want %v", counts, want)
	}
	for _, message := range want {
		if counts[message] != 1 {
			t.Fatalf("audit messages = %v, want %v", counts, want)
		}
	}
}
