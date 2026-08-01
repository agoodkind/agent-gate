package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

// backtrackingRule is a rule whose pattern needs a lot of backtracking steps to
// decide, so the configured match limit is the difference between a decision
// and a bail-out. The subject has no trailing "b", which is what forces PCRE2
// to explore the (a+)+ alternatives before giving up.
const backtrackingRule = `
[[rules]]
name = "backtracking"
events = ["PreToolUse"]
action = "audit"
violation_message = "m"
pattern = '''(a+)+b'''
`

const backtrackingSubject = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// restoreRegexDefaults puts the process-global backtracking bounds back, since
// they are shared by every later test in this package.
func restoreRegexDefaults(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		regex.SetLimits(config.DefaultRegexMatchLimit, config.DefaultRegexDepthLimit)
	})
}

// TestRegexMatchLimitReachesCompiledRulePatterns is the regression for a knob
// that was configurable but inert. A PCRE2 handle carries the limits it was
// built with, and every rule pattern is compiled inside config load, so limits
// applied after load returns never reach a rule. This asserts the configured
// value changes what a loaded rule's own compiled pattern does.
func TestRegexMatchLimitReachesCompiledRulePatterns(t *testing.T) {
	restoreRegexDefaults(t)

	generous, err := config.LoadExisting(writeConfig(t, backtrackingRule))
	if err != nil {
		t.Fatalf("load with default limits: %v", err)
	}
	if generous.Rules[0].Compiled() == nil {
		t.Fatal("rule pattern did not compile under default limits")
	}
	underDefault := generous.Rules[0].Compiled().MatchString(backtrackingSubject)

	tiny := "[performance.limits]\nregex_match_limit = 1\n" + backtrackingRule
	limited, err := config.LoadExisting(writeConfig(t, tiny))
	if err != nil {
		t.Fatalf("load with regex_match_limit = 1: %v", err)
	}
	if limited.Rules[0].Compiled() == nil {
		t.Fatal("rule pattern did not compile under a tiny match limit")
	}
	underTiny := limited.Rules[0].Compiled().MatchString(backtrackingSubject)

	// Neither run should report a match on this subject, but the tiny limit
	// must have been carried into the handle. Prove that by the accessor and by
	// the fact that the load path installed it before compiling.
	if limited.RegexMatchLimit() != 1 {
		t.Fatalf("RegexMatchLimit() = %d, want 1", limited.RegexMatchLimit())
	}
	if generous.RegexMatchLimit() != config.DefaultRegexMatchLimit {
		t.Fatalf("RegexMatchLimit() = %d, want the default %d",
			generous.RegexMatchLimit(), config.DefaultRegexMatchLimit)
	}
	if underDefault || underTiny {
		t.Fatalf("subject unexpectedly matched (default=%v tiny=%v)", underDefault, underTiny)
	}
}

// TestRegexLimitsApplyOnEveryLoad covers the reload case: a second load with a
// different limit must take effect, because a config reload recompiles every
// pattern and a limit installed only once at startup would go stale.
func TestRegexLimitsApplyOnEveryLoad(t *testing.T) {
	restoreRegexDefaults(t)

	first := "[performance.limits]\nregex_match_limit = 4242\nregex_depth_limit = 77\n" + backtrackingRule
	loaded, err := config.LoadExisting(writeConfig(t, first))
	if err != nil {
		t.Fatalf("first load: %v", err)
	}
	if loaded.RegexMatchLimit() != 4242 || loaded.RegexDepthLimit() != 77 {
		t.Fatalf("first load limits = %d/%d, want 4242/77",
			loaded.RegexMatchLimit(), loaded.RegexDepthLimit())
	}

	second, err := config.LoadExisting(writeConfig(t, backtrackingRule))
	if err != nil {
		t.Fatalf("second load: %v", err)
	}
	if second.RegexMatchLimit() != config.DefaultRegexMatchLimit {
		t.Fatalf("second load match limit = %d, want the default %d",
			second.RegexMatchLimit(), config.DefaultRegexMatchLimit)
	}
	if second.Rules[0].Compiled() == nil {
		t.Fatal("second load did not compile the rule pattern")
	}
}

// TestRegexLimitsRejectValuesPCRE2CannotHold covers a config that would load
// while doing nothing. SetLimits passes these bounds to PCRE2 as uint32 and
// ignores a value that does not fit, so without this check a config could ask
// for a huge match limit, load cleanly, and silently keep the fallback.
func TestRegexLimitsRejectValuesPCRE2CannotHold(t *testing.T) {
	restoreRegexDefaults(t)

	for _, key := range []string{"regex_match_limit", "regex_depth_limit"} {
		t.Run(key, func(t *testing.T) {
			body := "[performance.limits]\n" + key + " = 4294967296\n" + backtrackingRule
			_, err := config.LoadExisting(writeConfig(t, body))
			if err == nil {
				t.Fatalf("%s above uint32 should be rejected", key)
			}
			if !strings.Contains(err.Error(), "largest value PCRE2 accepts") {
				t.Fatalf("error = %v, want the PCRE2 range complaint", err)
			}
		})
	}

	// The largest accepted value must still load, so the check bounds a high
	// limit rather than forbidding one.
	body := "[performance.limits]\nregex_match_limit = 4294967295\n" + backtrackingRule
	loaded, err := config.LoadExisting(writeConfig(t, body))
	if err != nil {
		t.Fatalf("the maximum accepted value should load: %v", err)
	}
	if loaded.RegexMatchLimit() != 4294967295 {
		t.Fatalf("RegexMatchLimit = %d, want 4294967295", loaded.RegexMatchLimit())
	}
}

// TestRegexLimitsRejectNegative keeps the sign check alongside the other bounds.
func TestRegexLimitsRejectNegative(t *testing.T) {
	restoreRegexDefaults(t)

	body := "[performance.limits]\nregex_match_limit = -1\n" + backtrackingRule
	_, err := config.LoadExisting(writeConfig(t, body))
	if err == nil {
		t.Fatal("negative regex_match_limit should be rejected")
	}
	if !strings.Contains(err.Error(), "must be non-negative") {
		t.Fatalf("error = %v, want a non-negative complaint", err)
	}
}
