package config_test

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	body := "[performance.limits]\nregex_match_limit = 123\naudit_queue_limit = 456\n" +
		"[performance.intervals]\noverload_log_interval_ms = 789\n" + validExecRule
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting: %v", err)
	}
	if got := cfg.RegexMatchLimit(); got != 123 {
		t.Fatalf("RegexMatchLimit = %d, want 123", got)
	}
	if got := cfg.AuditQueueLimit(); got != 456 {
		t.Fatalf("AuditQueueLimit = %d, want 456", got)
	}
	if got := cfg.OverloadLogInterval().Milliseconds(); got != 789 {
		t.Fatalf("OverloadLogInterval = %dms, want 789ms", got)
	}
}

// TestEveryDeclaredKnobReachesAConsumer keeps [performance.limits] and
// [performance.intervals] honest. A key an operator can set that changes
// nothing is worse than no key, because setting it looks like it worked. Eleven
// such keys shipped once before this existed.
//
// No linter covers this. Measured on 2026-08-06 with a field declared, named in
// every struct literal, and given no accessor: make check passed clean.
// exhaustruct only fires until the field is added to the literals, and its
// remedy is to add it, which silences the check and leaves the knob inert.
// deadcode reports an accessor with no caller, and here there is no accessor.
//
// Rather than count fields, this sets each declared key to a distinctive value
// and requires some accessor to return it. A key with no reader fails by name,
// and the fix is to wire it or drop it rather than to update a number.
func TestEveryDeclaredKnobReachesAConsumer(t *testing.T) {
	for _, section := range []struct {
		table  string
		fields reflect.Type
	}{
		{"limits", reflect.TypeOf(config.LimitPerformance{})},
		{"intervals", reflect.TypeOf(config.IntervalPerformance{})},
	} {
		for index := range section.fields.NumField() {
			field := section.fields.Field(index)
			key := strings.Split(field.Tag.Get("toml"), ",")[0]
			if key == "" {
				t.Fatalf("%s.%s has no toml tag, so it cannot be configured",
					section.table, field.Name)
			}
			t.Run(section.table+"."+key, func(t *testing.T) {
				assertKnobIsRead(t, section.table, key)
			})
		}
	}
}

// knobProbeValue is distinctive enough that finding it in an accessor's return
// proves it came from the config rather than from a default.
const knobProbeValue = 4242

// largeKnobProbeValue is for a key whose validation requires it to exceed a
// companion key's default. deferred_claim_lease_ms must stay above
// deferred_claim_renew_ms, which defaults to 10000, so the small probe would be
// rejected by a rule that is working correctly.
const largeKnobProbeValue = 424242

// probeValueFor picks a probe that satisfies the cross-key rules a config
// enforces, so the test measures whether a key is read rather than re-testing
// validation that other tests already cover.
func probeValueFor(key string) int {
	if key == "deferred_claim_lease_ms" {
		return largeKnobProbeValue
	}
	return knobProbeValue
}

// assertKnobIsRead sets one key and requires an accessor to return it.
//
// The accessors are discovered by reflection rather than listed, so a knob
// added with no reader has nothing to find and fails, while a knob wired to a
// new accessor passes without this test needing an edit.
func assertKnobIsRead(t *testing.T, table string, key string) {
	t.Helper()
	probe := probeValueFor(key)
	body := "[performance." + table + "]\n" + key + " = " +
		strconv.Itoa(probe) + "\n" + validExecRule
	cfg, err := writeExecConfig(t, body)
	if err != nil {
		t.Fatalf("LoadExisting with %s = %d: %v", key, probe, err)
	}

	value := reflect.ValueOf(cfg)
	for index := range value.NumMethod() {
		method := value.Type().Method(index)
		if method.Type.NumIn() != 1 || method.Type.NumOut() != 1 {
			continue
		}
		switch returned := value.Method(index).Call(nil)[0].Interface().(type) {
		case int:
			if returned == probe {
				return
			}
		case time.Duration:
			// An interval is declared in milliseconds and returned as a
			// Duration, so the probe is compared in the unit it was written in.
			if returned.Milliseconds() == int64(probe) {
				return
			}
		}
	}
	t.Fatalf("performance.%s.%s decodes but no accessor returns it, so setting "+
		"it changes nothing. Wire it to the code that enforces it, or drop the key.",
		table, key)
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
	// hook_evaluate_ms has no Config accessor, because the hook reads it through
	// HookEvaluateTimeoutFromFile without loading rules. Its other role is the
	// bound the per-condition ceilings are validated against, covered by
	// TestExecCeilingMustStayUnderHookDeadline.
}
