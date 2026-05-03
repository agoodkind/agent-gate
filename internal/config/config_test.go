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
