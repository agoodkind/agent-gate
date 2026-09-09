package hook_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/audit"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

func TestWriteDeferredFindingsPersistsOnlyPhaseViolations(t *testing.T) {
	for _, profile := range []string{"full", "minimal"} {
		for _, decision := range []hook.ResponseDecision{hook.ResponseDecisionAllow, hook.ResponseDecisionBlock} {
			t.Run(profile+"/"+string(decision), func(t *testing.T) {
				rule := testProviderRule(t, "deferred-command", "echo audit", []string{"PreToolUse"}, []string{"tool_input.command"}, "Deferred command matched.")
				rule.AuditOnly = true
				cfg := &config.Config{Rules: []config.Rule{rule}}
				cfg.Audit.Storage.Profile = profile
				cfg.Audit.Outputs.SQLite.Path = filepath.Join(t.TempDir(), "audit.db")
				if err := cfg.PrepareAuditStorage(); err != nil {
					t.Fatal(err)
				}
				logger, err := audit.NewEventLoggerWithOptions(t.Context(), cfg, nil, audit.LoggerOptions{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = logger.Close() })
				raw := []byte(`{"session_id":"deferred-findings","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"echo audit"}}`)
				event := evaluateHot(t.Context(), raw, cfg, hook.SystemCodex, func(string) string { return "" }).Deferred
				if len(event.AuditOnlyViolations) != 1 {
					t.Fatalf("deferred evaluation = %+v", event)
				}
				event.Decision = decision
				event.ResponseEffects = []hook.ResponseEffectRecord{{
					RuleName: "prior-hot-effect", EffectType: "inject", Target: "context",
					ByteCount: 16, Disposition: "applied",
				}}
				sink := audit.NewLocalSink(logger)
				for range 2 {
					hook.WriteDeferredFindings(t.Context(), event, sink)
				}
				event.AuditOnlyViolations = nil
				hook.WriteDeferredFindings(t.Context(), event, sink)
				if err := logger.Close(); err != nil {
					t.Fatal(err)
				}
				events, _, err := audit.QueryReadOnly(t.Context(), cfg, audit.QueryFilter{})
				if err != nil {
					t.Fatal(err)
				}
				if len(events) != 1 || events[0].Message != "hook.audit_violation" {
					t.Fatalf("deferred events = %+v, want one finding without allow or effects", events)
				}
				got := events[0]
				if got.Decision.Kind != "audit_only" || !reflect.DeepEqual(got.Decision.RulesChecked, []string{"deferred-command"}) || !reflect.DeepEqual(got.Decision.RulesMatched, []string{"deferred-command"}) || len(got.Violations) != 1 || !strings.Contains(got.Violations[0].Message, "Deferred command matched.") {
					t.Fatalf("deferred finding = %+v", got)
				}
			})
		}
	}
}
