package main_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

func buildConfigCheckBinary(t *testing.T) string {
	t.Helper()
	binaryPath := filepath.Join(t.TempDir(), "bin", "agent-gate")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o700); err != nil {
		t.Fatalf("create binary directory: %v", err)
	}
	command := exec.CommandContext(t.Context(), "go", "build", "-o", binaryPath, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build agent-gate: %v\n%s", err, output)
	}
	return binaryPath
}

func writeConfigCheckRules(t *testing.T, configHome string, nameSuffix string) {
	t.Helper()
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

func runConfigCheck(t *testing.T, binaryPath string, configHome string) (int, string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), binaryPath, "config", "check")
	command.Env = append(
		os.Environ(),
		"XDG_CONFIG_HOME="+configHome,
		"XDG_STATE_HOME="+t.TempDir(),
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	err := command.Run()
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), stderr.String()
	}
	if err != nil {
		t.Fatalf("run agent-gate config check: %v", err)
	}
	return configCheckExitOK, stderr.String()
}

func TestConfigCheckThroughCLI(t *testing.T) {
	binaryPath := buildConfigCheckBinary(t)

	t.Run("rejects a rule set the store refuses", func(t *testing.T) {
		configHome := t.TempDir()
		writeConfigCheckRules(t, configHome, strings.Repeat("x", configCheckLongNameBytes))

		exitCode, stderr := runConfigCheck(t, binaryPath, configHome)

		if exitCode != configCheckExitFailed {
			t.Fatalf("exit code = %d, want %d; stderr = %q", exitCode, configCheckExitFailed, stderr)
		}
		if !strings.Contains(stderr, "bytes of rule-engine metadata") {
			t.Fatalf("stderr = %q, want the rule-engine metadata size", stderr)
		}
	})

	t.Run("accepts a recordable rule set", func(t *testing.T) {
		configHome := t.TempDir()
		writeConfigCheckRules(t, configHome, configCheckShortNameSuffix)

		exitCode, stderr := runConfigCheck(t, binaryPath, configHome)

		if exitCode != configCheckExitOK {
			t.Fatalf("exit code = %d, want %d; stderr = %q", exitCode, configCheckExitOK, stderr)
		}
	})
}
