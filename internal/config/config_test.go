package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
)

func TestLoadValidatesRuleDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "capture-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 1
action = "block"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].DiagnosticGroup != 1 {
		t.Fatalf("loaded diagnostic_group = %#v", cfg.Rules)
	}
}

func TestLoadDefaultsRuleDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "default-format"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %#v", cfg.Rules)
	}
	if cfg.Rules[0].DiagnosticFormat != config.DiagnosticFormatDetailed {
		t.Fatalf("DiagnosticFormat = %q, want %q", cfg.Rules[0].DiagnosticFormat, config.DiagnosticFormatDetailed)
	}
}

func TestLoadAcceptsMessageOnlyDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "message-only"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
diagnostic_format = "message_only"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %#v", cfg.Rules)
	}
	if cfg.Rules[0].DiagnosticFormat != config.DiagnosticFormatMessageOnly {
		t.Fatalf("DiagnosticFormat = %q, want %q", cfg.Rules[0].DiagnosticFormat, config.DiagnosticFormatMessageOnly)
	}
}

func TestLoadRejectsUnknownDiagnosticFormat(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "bad-format"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
diagnostic_format = "compact"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for unknown diagnostic_format")
	}
	if !strings.Contains(err.Error(), `unknown diagnostic_format "compact"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsInvalidDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "bad-group"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 2
action = "block"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for invalid diagnostic_group")
	}
	if !strings.Contains(err.Error(), "diagnostic_group 2 exceeds capture count 1") {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadValidatesConditionDiagnosticGroup(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "condition-capture-rule"
events = ["Stop"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
field_paths = ["assistant_message"]
pattern = "prefix (bad) suffix"
diagnostic_group = 1
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Conditions[0].DiagnosticGroup != 1 {
		t.Fatalf("loaded condition diagnostic_group = %#v", cfg.Rules)
	}
}

func TestLoadExistingRequiresFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent-gate", "config.toml")

	_, err := config.LoadExisting(path)
	if err == nil {
		t.Fatal("LoadExisting() returned nil error for missing config")
	}
	if !strings.Contains(err.Error(), "stat config") {
		t.Fatalf("LoadExisting() error = %v", err)
	}
}

func TestLoadTelemetryBlock(t *testing.T) {
	setConfigHome(t, `
[telemetry]
otlp_endpoint = "127.0.0.1:4317"
slow_op_threshold_ms = 50
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Telemetry.OTLPEndpoint != "127.0.0.1:4317" {
		t.Errorf("OTLPEndpoint = %q, want %q", cfg.Telemetry.OTLPEndpoint, "127.0.0.1:4317")
	}
	if cfg.Telemetry.SlowOpThresholdMs != 50 {
		t.Errorf("SlowOpThresholdMs = %d, want 50", cfg.Telemetry.SlowOpThresholdMs)
	}
}

func TestHookCachePerformanceDefaultsAndOverrides(t *testing.T) {
	setConfigHome(t, ``)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.HookCacheMaxEntries() != hotkv.DefaultMaxEntries {
		t.Fatalf("HookCacheMaxEntries = %d, want %d", cfg.HookCacheMaxEntries(), hotkv.DefaultMaxEntries)
	}
	if cfg.HookCacheMaxValueBytes() != hotkv.DefaultMaxValueBytes {
		t.Fatalf("HookCacheMaxValueBytes = %d, want %d", cfg.HookCacheMaxValueBytes(), hotkv.DefaultMaxValueBytes)
	}
	if cfg.HookCachePruneInterval() != hotkv.DefaultPruneInterval {
		t.Fatalf("HookCachePruneInterval = %s, want %s", cfg.HookCachePruneInterval(), hotkv.DefaultPruneInterval)
	}

	setConfigHome(t, `
[performance.hook.cache]
max_entries = 12
max_value_bytes = 34
prune_interval_ms = 56
`)
	cfg, err = config.Load()
	if err != nil {
		t.Fatalf("Load() override error: %v", err)
	}
	if cfg.HookCacheMaxEntries() != 12 {
		t.Fatalf("HookCacheMaxEntries override = %d, want 12", cfg.HookCacheMaxEntries())
	}
	if cfg.HookCacheMaxValueBytes() != 34 {
		t.Fatalf("HookCacheMaxValueBytes override = %d, want 34", cfg.HookCacheMaxValueBytes())
	}
	if cfg.HookCachePruneInterval() != 56*time.Millisecond {
		t.Fatalf("HookCachePruneInterval override = %s, want 56ms", cfg.HookCachePruneInterval())
	}
}

func TestLoadActionAuditSetsAuditOnly(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "audit-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "audit"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Action != config.ActionAudit {
		t.Fatalf("rule action = %q, want %q", cfg.Rules[0].Action, config.ActionAudit)
	}
	if !cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = false, want true")
	}
}

func TestLoadActionDefaultsToBlock(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "default-action-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Action != config.ActionBlock {
		t.Fatalf("rule action = %q, want %q", cfg.Rules[0].Action, config.ActionBlock)
	}
	if cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = true, want false for default action")
	}
}

func TestLoadRejectsUnknownAction(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "unknown-action-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "warn"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for unknown action")
	}
	if !strings.Contains(err.Error(), `unknown action "warn"`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadExampleConfig(t *testing.T) {
	examplePath := filepath.Join("..", "..", "config.toml.example")

	cfg, err := config.LoadExisting(examplePath)
	if err != nil {
		t.Fatalf("LoadExisting(%q) error: %v", examplePath, err)
	}
	if len(cfg.Rules) == 0 {
		t.Fatal("example config loaded zero rules")
	}
}

func setConfigHome(t *testing.T, contents string) {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}
