package config_test

import (
	"strconv"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// TestExecTimeoutCeilingFitsHookDeadline pins the exec timeout ceiling against
// the hook client deadline it exists to protect. A validator that outlives the
// deadline makes the hook give up and the rule fail open, losing its verdict,
// so the ceiling must stay strictly under evaluateHookTimeout in
// internal/daemon/client.go. That deadline is 12s.
func TestExecTimeoutCeilingFitsHookDeadline(t *testing.T) {
	const hookDeadlineMs = 12000

	if config.MaxExecTimeoutMs >= hookDeadlineMs {
		t.Fatalf(
			"MaxExecTimeoutMs %d must stay under the %dms hook deadline",
			config.MaxExecTimeoutMs,
			hookDeadlineMs,
		)
	}
	if config.DefaultExecTimeoutMs > config.MaxExecTimeoutMs {
		t.Fatalf(
			"DefaultExecTimeoutMs %d exceeds the ceiling %d",
			config.DefaultExecTimeoutMs,
			config.MaxExecTimeoutMs,
		)
	}
}

// TestExecConditionAcceptsCeilingAndRejectsAbove covers both sides of the
// bound, so raising the ceiling cannot silently stop being enforced.
func TestExecConditionAcceptsCeilingAndRejectsAbove(t *testing.T) {
	atCeiling := strings.Replace(
		validExecRule,
		`command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\ntimeout_ms = "+strconv.Itoa(config.MaxExecTimeoutMs),
		1,
	)
	cfg, err := writeExecConfig(t, atCeiling)
	if err != nil {
		t.Fatalf("timeout_ms at the ceiling should load: %v", err)
	}
	if got := cfg.Rules[0].Conditions[1].TimeoutMs; got != config.MaxExecTimeoutMs {
		t.Fatalf("timeout_ms = %d, want %d", got, config.MaxExecTimeoutMs)
	}

	aboveCeiling := strings.Replace(
		validExecRule,
		`command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\ntimeout_ms = "+strconv.Itoa(config.MaxExecTimeoutMs+1),
		1,
	)
	if _, err := writeExecConfig(t, aboveCeiling); err == nil {
		t.Fatal("timeout_ms above the ceiling should be rejected")
	}
}
