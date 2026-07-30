package config_test

import (
	"strconv"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// TestExecTimeoutCeilingFitsHookDeadline pins the default ceiling against the
// default hook deadline it exists to protect. A validator that outlives the
// deadline makes the hook give up and the rule fail open, losing its verdict.
func TestExecTimeoutCeilingFitsHookDeadline(t *testing.T) {
	if config.DefaultMaxExecTimeoutMs >= config.DefaultHookEvaluateMs {
		t.Fatalf(
			"DefaultMaxExecTimeoutMs %d must stay under DefaultHookEvaluateMs %d",
			config.DefaultMaxExecTimeoutMs,
			config.DefaultHookEvaluateMs,
		)
	}
	if config.DefaultExecTimeoutMs > config.DefaultMaxExecTimeoutMs {
		t.Fatalf(
			"DefaultExecTimeoutMs %d exceeds the ceiling %d",
			config.DefaultExecTimeoutMs,
			config.DefaultMaxExecTimeoutMs,
		)
	}
}

func execRuleWith(extra string) string {
	return strings.Replace(
		validExecRule,
		`command = ["/bin/true"]`,
		`command = ["/bin/true"]`+"\n"+extra,
		1,
	)
}

// TestExecConditionAcceptsCeilingAndRejectsAbove covers both sides of the
// bound, so raising the ceiling cannot silently stop being enforced.
func TestExecConditionAcceptsCeilingAndRejectsAbove(t *testing.T) {
	atCeiling := execRuleWith("timeout_ms = " + strconv.Itoa(config.DefaultMaxExecTimeoutMs))
	cfg, err := writeExecConfig(t, atCeiling)
	if err != nil {
		t.Fatalf("timeout_ms at the ceiling should load: %v", err)
	}
	if got := cfg.Rules[0].Conditions[1].TimeoutMs; got != config.DefaultMaxExecTimeoutMs {
		t.Fatalf("timeout_ms = %d, want %d", got, config.DefaultMaxExecTimeoutMs)
	}

	aboveCeiling := execRuleWith("timeout_ms = " + strconv.Itoa(config.DefaultMaxExecTimeoutMs+1))
	if _, err := writeExecConfig(t, aboveCeiling); err == nil {
		t.Fatal("timeout_ms above the ceiling should be rejected")
	}
}

// TestConfigRaisesItsOwnExecCeiling covers the property that made this change
// necessary: a config wanting a longer validator timeout raises the ceiling in
// the same file, so it can never require a binary whose ceiling has not shipped.
func TestConfigRaisesItsOwnExecCeiling(t *testing.T) {
	body := "[performance.timeouts]\nexec_max_ms = 9000\n" + execRuleWith("timeout_ms = 9000")
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("config raising its own ceiling should load: %v", err)
	}
	if got := cfg.Rules[0].Conditions[1].TimeoutMs; got != 9000 {
		t.Fatalf("timeout_ms = %d, want 9000", got)
	}
	if got := cfg.ExecMaxTimeoutMs(); got != 9000 {
		t.Fatalf("ExecMaxTimeoutMs = %d, want 9000", got)
	}
}

// TestExecCeilingMustStayUnderHookDeadline covers the relationship a config can
// no longer break: a ceiling at or above the hook deadline lets a condition
// outlive the evaluation and fail open.
func TestExecCeilingMustStayUnderHookDeadline(t *testing.T) {
	body := "[performance.timeouts]\nhook_evaluate_ms = 5000\nexec_max_ms = 5000\n" + validExecRule
	_, err := writeExecConfig(t, body)
	if err == nil {
		t.Fatal("exec_max_ms equal to hook_evaluate_ms should be rejected")
	}
	if !strings.Contains(err.Error(), "must stay under hook_evaluate_ms") {
		t.Fatalf("error = %v, want the hook-deadline relationship", err)
	}
}

// TestTimeoutsRejectNegative covers the sign check on every timeout knob.
func TestTimeoutsRejectNegative(t *testing.T) {
	body := "[performance.timeouts]\nexec_background_ms = -1\n" + validExecRule
	_, err := writeExecConfig(t, body)
	if err == nil || !strings.Contains(err.Error(), "must be non-negative") {
		t.Fatalf("error = %v, want a non-negative complaint", err)
	}
}

// TestLimitsAndIntervalsAreConfigurable covers that the size and period knobs
// reach their accessors rather than staying fixed at build time.
func TestLimitsAndIntervalsAreConfigurable(t *testing.T) {
	body := "[performance.limits]\nregex_match_limit = 123\nshell_read_max_bytes = 456\n" +
		"[performance.intervals]\noverload_log_interval_ms = 789\n" + validExecRule
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if got := cfg.RegexMatchLimit(); got != 123 {
		t.Fatalf("RegexMatchLimit = %d, want 123", got)
	}
	if got := cfg.ShellReadMaxBytes(); got != 456 {
		t.Fatalf("ShellReadMaxBytes = %d, want 456", got)
	}
	if got := cfg.OverloadLogInterval().Milliseconds(); got != 789 {
		t.Fatalf("OverloadLogInterval = %dms, want 789ms", got)
	}
}

// TestUnsetKnobsTakeDefaults covers that an empty section still produces every
// documented default, so adding the knobs changed no existing behavior.
func TestUnsetKnobsTakeDefaults(t *testing.T) {
	cfg, err := writeExecConfig(t, validExecRule)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	checks := []struct {
		name string
		got  int
		want int
	}{
		{"ExecMaxTimeoutMs", cfg.ExecMaxTimeoutMs(), config.DefaultMaxExecTimeoutMs},
		{"ExecDefaultTimeoutMs", cfg.ExecDefaultTimeoutMs(), config.DefaultExecTimeoutMs},
		{"ExecMaxRetryCount", cfg.ExecMaxRetryCount(), config.DefaultMaxExecRetryCount},
		{"InferMaxTimeoutMs", cfg.InferMaxTimeoutMs(), config.DefaultMaxInferTimeoutMs},
		{"RegexMatchLimit", cfg.RegexMatchLimit(), config.DefaultRegexMatchLimit},
		{"AuditQueueLimit", cfg.AuditQueueLimit(), config.DefaultAuditQueueLimit},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %d, want default %d", check.name, check.got, check.want)
		}
	}
	if got := cfg.HookEvaluateTimeout().Milliseconds(); got != config.DefaultHookEvaluateMs {
		t.Fatalf("HookEvaluateTimeout = %dms, want %dms", got, config.DefaultHookEvaluateMs)
	}
}
