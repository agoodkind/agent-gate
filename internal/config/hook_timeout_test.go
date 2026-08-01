package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// TestHookEvaluateTimeoutReadsOnlyThePerformanceTable is the regression for a
// hook that started compiling rules. The hook process is transport-only, so the
// deadline lookup must not go through Load, which decodes the whole file and
// compiles every rule pattern through PCRE2 on the hook's critical path.
//
// A config whose rules cannot compile proves it: Load would fail on it, so a
// value still coming back means the rules were never touched.
func TestHookEvaluateTimeoutReadsOnlyThePerformanceTable(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", directory)
	configDirectory := filepath.Join(directory, "agent-gate")
	if err := os.MkdirAll(configDirectory, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	body := `
[performance.timeouts]
hook_evaluate_ms = 9500

[[rules]]
name = "uncompilable"
events = ["PreToolUse"]
action = "audit"
violation_message = "m"
pattern = '''(unclosed'''
`
	if err := os.WriteFile(filepath.Join(configDirectory, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Load must reject this file, because the rule pattern cannot compile.
	if _, err := config.Load(); err == nil {
		t.Fatal("Load accepted a config whose rule pattern cannot compile")
	}

	// The hook lookup must still answer, which it can only do by skipping rules.
	got := config.HookEvaluateTimeoutFromFile()
	if got != 9500*time.Millisecond {
		t.Fatalf("HookEvaluateTimeoutFromFile() = %s, want 9500ms", got)
	}
}
