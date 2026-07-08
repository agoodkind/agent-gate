package config_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestComposerConditionRequiresRuleSetID(t *testing.T) {
	_, err := writeExecConfig(t, `
[[rules]]
name = "composer-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "composer"
`)
	if err == nil || !strings.Contains(err.Error(), "composer requires rule_set_id") {
		t.Fatalf("expected rule_set_id validation error, got %v", err)
	}
}

func TestComposerConditionCompiles(t *testing.T) {
	cfg, err := writeExecConfig(t, `
[[rules]]
name = "composer-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "composer"
rule_set_id = "search-guard"
`)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	condition := cfg.Rules[0].Conditions[0]
	if condition.Kind != string(config.ConditionKindComposer) {
		t.Fatalf("Kind = %q, want composer", condition.Kind)
	}
	if condition.RuleSetID != "search-guard" {
		t.Fatalf("RuleSetID = %q, want search-guard", condition.RuleSetID)
	}
}
