package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func writeUnusableConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// unparseable is missing a closing bracket, so no part of it decodes.
const unparseable = `
[[rules]]
name = "broken"
events = ["PreToolUse"
`

// wrongType decodes as TOML but assigns a boolean where the schema wants a
// string, which is what a config written for a different binary version looks
// like. It took the daemon down on 2026-08-02.
const wrongType = `
[[rules]]
name = "version-mismatch"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''x'''

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
stdout_json_field = "ok"
stdout_json_equals = true
`

// TestADecodeFailureStartsWithNothingRatherThanRefusing is the regression for a
// load that killed the daemon.
//
// Refusing to start preserves no enforcement. Measured on 2026-08-05: with a
// corrupt config the daemon exited, and a guarded grep was then allowed through
// because the hook found no daemon. Starting with no rules is the same
// enforcement, and it leaves a process alive that can report the outage.
func TestADecodeFailureStartsWithNothingRatherThanRefusing(t *testing.T) {
	for _, testCase := range []struct {
		name string
		body string
	}{
		{"unparseable", unparseable},
		{"wrong type for the schema", wrongType},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			path := writeUnusableConfig(t, testCase.body)

			// Strict load still refuses it, so install-time validation catches
			// the mistake before it reaches a running daemon.
			if _, err := config.LoadExisting(path); err == nil {
				t.Fatal("strict load accepted a config that does not decode")
			}

			cfg, err := config.LoadDegradedPath(path)
			if err != nil {
				t.Fatalf("degraded load refused to start: %v", err)
			}
			if !cfg.Unusable() {
				t.Fatal("Unusable() = false, so nothing downstream knows enforcement is gone")
			}
			if len(cfg.Rules) != 0 {
				t.Fatalf("rules = %d, want none; a partial decode must not reach enforcement",
					len(cfg.Rules))
			}
			failures := cfg.Failures()
			if len(failures) != 1 || failures[0].Kind != config.LoadFailureDocument {
				t.Fatalf("failures = %v, want one document failure", failures)
			}
			if !strings.Contains(failures[0].Scope, "config.toml") {
				t.Fatalf("failure scope = %q, want the config path", failures[0].Scope)
			}
		})
	}
}

// TestAUsableConfigIsNotReportedUnusable keeps the signal meaningful. A file
// that decodes is never unusable, even when it lost a rule along the way.
func TestAUsableConfigIsNotReportedUnusable(t *testing.T) {
	body := `
[[rules]]
name = "good"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''secret'''

[[rules]]
name = "bad-pattern"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''['''
`
	cfg, err := config.LoadDegradedPath(writeUnusableConfig(t, body))
	if err != nil {
		t.Fatalf("degraded load: %v", err)
	}
	if cfg.Unusable() {
		t.Fatal("a file that decoded was reported unusable")
	}
	if !cfg.Degraded() {
		t.Fatal("Degraded() = false after dropping a rule")
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules = %d, want the one that compiles", len(cfg.Rules))
	}
}

// TestAnAbsentConfigIsNotUnusable separates "no file" from "broken file". No
// file is the first-run case and carries no failure.
func TestAnAbsentConfigIsNotUnusable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := config.LoadDegraded()
	if err != nil {
		t.Fatalf("degraded load with no file: %v", err)
	}
	if cfg.Unusable() || cfg.Degraded() {
		t.Fatalf("an absent config reported unusable=%v degraded=%v",
			cfg.Unusable(), cfg.Degraded())
	}
}
