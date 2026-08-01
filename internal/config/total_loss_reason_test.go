package config_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// TestTotalLossNamesTheRuleErrorNotASectionError is the regression for an error
// that quoted the wrong reason. Section validation runs first and records its
// own failure, so indexing the combined list for the total-loss message could
// report an unrelated settings problem as the reason no rule compiled.
func TestTotalLossNamesTheRuleErrorNotASectionError(t *testing.T) {
	body := `
[performance.timeouts]
exec_max_ms = 999999

[[rules]]
name = "only-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''['''
`
	_, err := config.LoadDegradedPath(writeDegradedConfig(t, body))
	if err == nil {
		t.Fatal("a config whose every rule failed was accepted")
	}
	if !strings.Contains(err.Error(), "none compiled") {
		t.Fatalf("error = %v, want it to say no rule compiled", err)
	}
	if strings.Contains(err.Error(), "exec_max_ms") {
		t.Fatalf("error = %v, want the rule's own reason, not the settings block", err)
	}
	if !strings.Contains(err.Error(), "only-rule") {
		t.Fatalf("error = %v, want it to name the rule that failed", err)
	}
}
