package config_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
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
	if cond.RetryCount != 0 {
		t.Fatalf("expected default retry_count 0, got %d", cond.RetryCount)
	}
}

func TestExecConditionRetryCountPreservedWhenValid(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\nretry_count = 1", 1)
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if got := cfg.Rules[0].Conditions[1].RetryCount; got != 1 {
		t.Fatalf("expected retry_count 1 preserved, got %d", got)
	}
}

func TestExecConditionRejectsInvalidRetryCount(t *testing.T) {
	cases := []struct {
		name  string
		value string
	}{
		{"negative", "-1"},
		{"over max", strconv.Itoa(config.DefaultMaxExecRetryCount + 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
				`command = ["/bin/true"]`+"\nretry_count = "+tc.value, 1)
			if _, err := writeExecConfig(t, body); err == nil {
				t.Fatalf("expected retry_count %s to be rejected", tc.value)
			}
		})
	}
}

func TestExecConditionCacheKeyCmdReadTargetsCompiles(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\ncache_key = \"cmd_read_targets\"\nsearch_tools = [\"grep\", \"rg\"]", 1)
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	cond := cfg.Rules[0].Conditions[1]
	if cond.CacheKeySelector().Selector != config.FieldCmdReadTargets {
		t.Fatalf("expected cache_key to compile to cmd_read_targets selector")
	}
	if len(cond.SearchTools) != 2 || cond.SearchTools[0] != "grep" {
		t.Fatalf("expected search_tools preserved, got %v", cond.SearchTools)
	}
}

func TestExecConditionCacheKeyExecTargetsCompilesWithoutSearchTools(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\ncache_key = \"exec_targets\"", 1)
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	cond := cfg.Rules[0].Conditions[1]
	if cond.CacheKeySelector().Selector != config.FieldExecTargets {
		t.Fatalf("expected cache_key to compile to exec_targets selector")
	}
}

// The search-tool set is rule policy with no built-in default, so a
// cmd_read_targets cache key without search_tools is a config error rather
// than a silently empty key.
func TestExecConditionCmdReadTargetsCacheKeyRequiresSearchTools(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\ncache_key = \"cmd_read_targets\"", 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "requires search_tools") {
		t.Fatalf("expected search_tools validation error, got %v", err)
	}
}

func TestExecConditionEmptySearchToolEntryFails(t *testing.T) {
	body := strings.Replace(validExecRule, `command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\nsearch_tools = [\"grep\", \" \"]", 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "search_tools entries must be non-empty") {
		t.Fatalf("expected empty-entry validation error, got %v", err)
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

func TestExecConditionTemporalSelectorsRequireFieldPaths(t *testing.T) {
	tests := []struct {
		name  string
		field string
	}{
		{name: "cache key", field: `cache_key = "last_user_message"`},
		{name: "for each", field: `for_each = "last_response_output"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(
				validExecRule,
				`command = ["/bin/true"]`,
				`command = ["/bin/true"]`+"\n"+test.field,
				1,
			)
			_, err := writeExecConfig(t, body)
			if err == nil || !strings.Contains(err.Error(), "field_paths") {
				t.Fatalf("expected temporal selector scoping error, got %v", err)
			}
		})
	}
}

func TestExecConditionWarnsWhenCachingTemporalSelector(t *testing.T) {
	var logOutput bytes.Buffer
	previousLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logOutput, nil)))
	t.Cleanup(func() {
		slog.SetDefault(previousLogger)
	})

	body := strings.Replace(
		validExecRule,
		`command = ["/bin/true"]`,
		`command = ["/bin/true"]`+
			"\nfield_paths = [\"last_user_message\"]"+
			"\ncache_ttl_ms = 1000",
		1,
	)
	if _, err := writeExecConfig(t, body); err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}

	warning := logOutput.String()
	if !strings.Contains(warning, `"level":"WARN"`) {
		t.Fatalf("expected warning log, got %q", warning)
	}
	if !strings.Contains(warning, `"rule":"exec-rule"`) {
		t.Fatalf("warning does not name rule: %q", warning)
	}
	if !strings.Contains(warning, `"condition_index":1`) {
		t.Fatalf("warning does not name condition: %q", warning)
	}
}

func TestUnknownConditionKindFails(t *testing.T) {
	body := strings.Replace(validExecRule, `kind = "exec"`, `kind = "totally_unknown"`, 1)
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "unknown kind") {
		t.Fatalf("expected unknown-kind error, got %v", err)
	}
}
