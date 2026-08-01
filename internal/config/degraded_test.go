package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func writeDegradedConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// goodRule and outageRule reproduce the 2026-07-31 outage. The second rule asks
// for a validator timeout above the compiled ceiling, which is the exact value
// that failed validation, dropped all 73 rules, and left the daemon exiting 1
// on every launchd respawn for ten hours.
const goodRule = `
[[rules]]
name = "keeps-working"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''secret'''
`

const outageRule = `
[[rules]]
name = "asks-too-much"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''anything'''

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
timeout_ms = 999999
`

// TestOutageConfigNoLongerCostsEveryRule is the regression for the incident.
// The same file that took the gate down must now load, keep every rule that
// compiles, and name the one it dropped.
func TestOutageConfigNoLongerCostsEveryRule(t *testing.T) {
	path := writeDegradedConfig(t, goodRule+outageRule)

	// Strict load still refuses it, so install-time validation catches the
	// mistake before it ever reaches a running daemon.
	if _, err := config.LoadExisting(path); err == nil {
		t.Fatal("strict load accepted a rule whose timeout exceeds the ceiling")
	}

	cfg, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatalf("degraded load should not fail: %v", err)
	}

	if len(cfg.Rules) != 1 {
		names := make([]string, 0, len(cfg.Rules))
		for i := range cfg.Rules {
			names = append(names, cfg.Rules[i].Name)
		}
		t.Fatalf("rules kept = %v, want only keeps-working", names)
	}
	if cfg.Rules[0].Name != "keeps-working" {
		t.Fatalf("kept rule = %q, want keeps-working", cfg.Rules[0].Name)
	}
	if cfg.Rules[0].Compiled() == nil {
		t.Fatal("the surviving rule did not compile")
	}

	failures := cfg.Failures()
	if len(failures) != 1 {
		t.Fatalf("failures = %d, want 1: %v", len(failures), failures)
	}
	if failures[0].Kind != config.LoadFailureRule || failures[0].Scope != "asks-too-much" {
		t.Fatalf("failure = %+v, want the asks-too-much rule", failures[0])
	}
	if !strings.Contains(failures[0].Reason, "timeout_ms") {
		t.Fatalf("failure reason = %q, want it to name timeout_ms", failures[0].Reason)
	}
	if !cfg.Degraded() {
		t.Fatal("Degraded() = false after dropping a rule")
	}
}

// TestBadSettingsBlockFallsBackToDefaults covers the other half of the outage
// class: a settings block that will not validate must fall back to its
// documented defaults rather than taking every rule down with it.
func TestBadSettingsBlockFallsBackToDefaults(t *testing.T) {
	body := "[performance.timeouts]\nexec_max_ms = 999999\n" + goodRule
	path := writeDegradedConfig(t, body)

	if _, err := config.LoadExisting(path); err == nil {
		t.Fatal("strict load accepted a ceiling above the hook deadline")
	}

	cfg, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatalf("degraded load should not fail: %v", err)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("rules kept = %d, want 1", len(cfg.Rules))
	}
	if got := cfg.ExecMaxTimeoutMs(); got != config.DefaultMaxExecTimeoutMs {
		t.Fatalf("ExecMaxTimeoutMs = %d, want the default %d after fallback",
			got, config.DefaultMaxExecTimeoutMs)
	}
	failures := cfg.Failures()
	if len(failures) != 1 || failures[0].Kind != config.LoadFailureSection {
		t.Fatalf("failures = %v, want one section failure", failures)
	}
	if failures[0].Scope != "performance.timeouts" {
		t.Fatalf("failure scope = %q, want performance.timeouts", failures[0].Scope)
	}
}

// TestTotalRuleLossIsRefusedEvenDegraded draws the line the degraded path needs.
// Dropping some rules is degradation and is accepted. Dropping every rule is a
// broken file, and accepting it would leave the daemon enforcing nothing, which
// is the outage this path exists to prevent, just without a crash to reveal it.
// Refusing lets a running daemon keep its previous rule set.
func TestTotalRuleLossIsRefusedEvenDegraded(t *testing.T) {
	body := `
[[rules]]
name = "only-rule"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''['''
`
	path := writeDegradedConfig(t, body)

	_, err := config.LoadDegradedPath(path)
	if err == nil {
		t.Fatal("a config whose every rule failed was accepted; enforcement would be empty")
	}
	if !strings.Contains(err.Error(), "none compiled") {
		t.Fatalf("error = %v, want it to say no rule compiled", err)
	}
	if !strings.Contains(err.Error(), "keeping the previous config") {
		t.Fatalf("error = %v, want it to say the previous config is kept", err)
	}
}

// TestPartialLossIsAcceptedWhenOneRuleSurvives is the boundary case for the
// rule above: a single surviving rule is enough to accept the load.
func TestPartialLossIsAcceptedWhenOneRuleSurvives(t *testing.T) {
	broken := `
[[rules]]
name = "broken"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''['''
`
	cfg, err := config.LoadDegradedPath(writeDegradedConfig(t, goodRule+broken))
	if err != nil {
		t.Fatalf("one surviving rule should be accepted: %v", err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Name != "keeps-working" {
		t.Fatalf("rules = %d, want the one that compiles", len(cfg.Rules))
	}
}

// TestCleanConfigReportsNoFailures keeps the degraded path honest: a valid file
// must load identically either way and report nothing dropped.
func TestCleanConfigReportsNoFailures(t *testing.T) {
	path := writeDegradedConfig(t, goodRule)

	strict, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("strict load of a clean config: %v", err)
	}
	degraded, err := config.LoadDegradedPath(path)
	if err != nil {
		t.Fatalf("degraded load of a clean config: %v", err)
	}
	if len(strict.Rules) != len(degraded.Rules) {
		t.Fatalf("rule counts differ: strict %d, degraded %d",
			len(strict.Rules), len(degraded.Rules))
	}
	if degraded.Degraded() || len(degraded.Failures()) != 0 {
		t.Fatalf("clean config reported failures: %v", degraded.Failures())
	}
}
