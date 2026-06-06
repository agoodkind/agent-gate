package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func writeExecConfig(t *testing.T, body string) (*config.Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return config.LoadExisting(path)
}

const validExecRule = `
[[rules]]
name = "exec-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "regex"
field_paths = ["tool_input.command"]
pattern = "grep"

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
`

func TestExecConditionAppliesDefaults(t *testing.T) {
	cfg, err := writeExecConfig(t, validExecRule)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	cond := cfg.Rules[0].Conditions[1]
	if cond.TimeoutMs != config.DefaultExecTimeoutMs {
		t.Fatalf("expected default timeout %d, got %d", config.DefaultExecTimeoutMs, cond.TimeoutMs)
	}
	if cond.BlockOn != config.BlockOnNonzero {
		t.Fatalf("expected default block_on %q, got %q", config.BlockOnNonzero, cond.BlockOn)
	}
	if cond.OnError != config.OnErrorOpen {
		t.Fatalf("expected default on_error %q, got %q", config.OnErrorOpen, cond.OnError)
	}
	if cond.CacheKey != config.DefaultExecCacheKey {
		t.Fatalf("expected default cache_key %q, got %q", config.DefaultExecCacheKey, cond.CacheKey)
	}
	if cond.CacheKeySelector().Selector != config.FieldEffectiveCWD {
		t.Fatalf("expected cache key selector to compile to effective_cwd")
	}
}

func TestExecConditionMissingCommandFails(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`, "", 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "exec requires a non-empty command") {
		t.Fatalf("expected missing-command error, got %v", err)
	}
}

func TestExecConditionInvalidBlockOnFails(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`, `command = ["/bin/true"]`+"\nblock_on = \"maybe\"", 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "block_on") {
		t.Fatalf("expected block_on validation error, got %v", err)
	}
}

func TestExecConditionInvalidOnErrorFails(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`, `command = ["/bin/true"]`+"\non_error = \"halfway\"", 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "on_error") {
		t.Fatalf("expected on_error validation error, got %v", err)
	}
}

func TestUnknownConditionKindFails(t *testing.T) {
	body := strings.Replace(validExecRule, `kind = "exec"`, `kind = "totally_unknown"`, 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected unknown-kind error, got %v", err)
	}
}
