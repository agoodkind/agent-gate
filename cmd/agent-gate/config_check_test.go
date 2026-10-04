package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	configCheckRuleCount       = 100
	configCheckLongNameBytes   = 100 * 1024
	configCheckExitOK          = 0
	configCheckExitFailed      = 1
	configCheckShortNameSuffix = "short"
)

func writeConfigCheckRules(t *testing.T, nameSuffix string) {
	t.Helper()
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	var body strings.Builder
	for index := range configCheckRuleCount {
		fmt.Fprintf(&body, `[[rules]]
name = "canary-rule-%03d-%s"
events = ["PreToolUse"]
field_paths = ["tool_input.command"]
pattern = "canary-pattern"
action = "block"
violation_message = "blocked"

`, index, nameSuffix)
	}
	configDir := filepath.Join(configHome, "agent-gate")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte(body.String()), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func TestConfigCheckRejectsRuleSetTheStoreRefuses(t *testing.T) {
	writeConfigCheckRules(t, strings.Repeat("x", configCheckLongNameBytes))

	if exitCode := runConfig([]string{"check"}); exitCode != configCheckExitFailed {
		t.Fatalf("config check exit = %d, want %d", exitCode, configCheckExitFailed)
	}
}

func TestConfigCheckAcceptsRecordableRuleSet(t *testing.T) {
	writeConfigCheckRules(t, configCheckShortNameSuffix)

	if exitCode := runConfig([]string{"check"}); exitCode != configCheckExitOK {
		t.Fatalf("config check exit = %d, want %d", exitCode, configCheckExitOK)
	}
}
