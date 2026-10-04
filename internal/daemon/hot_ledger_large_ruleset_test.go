package daemon

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

// largeRuleSetSize produces rule-engine layer metadata above 64 KiB.
const largeRuleSetSize = 600

func TestEvaluateHookEnforcesBlockWithLargeRuleSet(t *testing.T) {
	setDaemonTestDirs(t)
	cfg := daemonTestConfig(t)
	unsubscribedPattern := regex.MustCompile(`canary-pattern-without-a-match`)
	for index := range largeRuleSetSize {
		cfg.Rules = append(cfg.Rules, config.NewSimpleRule(
			fmt.Sprintf("unsubscribed-canary-rule-with-a-long-identifier-%04d", index),
			`canary-pattern-without-a-match`,
			unsubscribedPattern,
			[]string{"PostToolUse"},
			[]string{"tool_input.command"},
			"block",
			"Canary rule for a large rule set.",
		))
	}
	server, err := newReadyTestServer(newDiscardLogger(), cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()

	response, err := server.EvaluateHook(context.Background(), blockingLedgerRequest(t))
	if err != nil {
		t.Fatalf("EvaluateHook: %v", err)
	}

	body := string(response.StdoutData) + string(response.StderrData)
	if strings.Contains(body, "no rule was enforced") {
		t.Fatalf("response = %q, want an enforced block for a large rule set", body)
	}
	if !strings.Contains(body, "Use make test for full project runs.") {
		t.Fatalf("response = %q, want the blocking rule message", body)
	}
	summary, err := FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Count != 0 {
		t.Fatalf("fail-open records = %d, want 0", summary.Count)
	}
}
