package daemon_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/api/daemonpb"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/daemon"
	"goodkind.io/agent-gate/internal/regex"
)

// largeRuleSetSize produces rule-engine layer metadata above 64 KiB.
const largeRuleSetSize = 600

func TestEvaluateHookEnforcesBlockWithLargeRuleSet(t *testing.T) {
	testRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(testRoot, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(testRoot, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(testRoot, "runtime"))

	blockingRule := config.NewSimpleRule(
		"no-broad-go-test",
		`go test \./\.\.\.`,
		regex.MustCompile(`go test \./\.\.\.`),
		nil,
		[]string{"tool_input.command"},
		"block",
		"Use make test for full project runs.",
	)
	blockingRule.AllEvents = true
	auditDisabled := false
	cfg := &config.Config{
		Audit: config.Audit{Enabled: &auditDisabled},
		Rules: []config.Rule{blockingRule},
	}
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
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	server, err := daemon.New(context.Background(), logger, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer server.Close()
	server.StartAuditScheduler(context.Background())

	response, err := server.EvaluateHook(context.Background(), &daemonpb.EvaluateHookRequest{
		RawJson:        []byte(`{"session_id":"ledger-session","hook_event_name":"PreToolUse","tool_name":"Shell","tool_input":{"command":"go test ./..."}}`),
		ProviderHint:   "codex",
		Cwd:            t.TempDir(),
		EnvFingerprint: map[string]string{"CODEX_THREAD_ID": "ledger-thread"},
	})
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
	summary, err := daemon.FailOpenRecordSummary()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary.Count != 0 {
		t.Fatalf("fail-open records = %d, want 0", summary.Count)
	}
}
