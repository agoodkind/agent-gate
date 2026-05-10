package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
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

func TestLoadDefaultsRuleClassFromAuditOnly(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "deferred-audit-rule"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
audit_only = true
violation_message = "blocked"
`)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Class != config.RuleClassDeferred {
		t.Fatalf("rule class = %q, want %q", cfg.Rules[0].Class, config.RuleClassDeferred)
	}
	if !cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = false, want true")
	}
}

func TestLoadMapsDeferredClassToAuditOnly(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "explicit-deferred-rule"
class = "deferred"
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
		t.Fatalf("loaded rules = %d, want 1", len(cfg.Rules))
	}
	if cfg.Rules[0].Class != config.RuleClassDeferred {
		t.Fatalf("rule class = %q, want %q", cfg.Rules[0].Class, config.RuleClassDeferred)
	}
	if !cfg.Rules[0].AuditOnly {
		t.Fatal("AuditOnly = false, want true")
	}
}

func TestLoadRejectsConflictingSyncClassAndAuditOnly(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "conflicting-sync-rule"
class = "sync"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
audit_only = true
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for conflicting class/audit_only")
	}
	if !strings.Contains(err.Error(), `class "sync" conflicts with audit_only = true`) {
		t.Fatalf("Load() error = %v", err)
	}
}

func TestLoadRejectsUnknownRuleClass(t *testing.T) {
	setConfigHome(t, `[[rules]]
name = "unknown-class-rule"
class = "later"
events = ["Stop"]
field_paths = ["assistant_message"]
pattern = "blocked"
action = "block"
violation_message = "blocked"
`)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() returned nil error for unknown class")
	}
	if !strings.Contains(err.Error(), `unknown class "later"`) {
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
