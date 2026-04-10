package rules_test

import (
	"regexp"
	"testing"

	"github.com/agoodkind/agent-gate/internal/config"
	"github.com/agoodkind/agent-gate/internal/rules"
)

// makeRule constructs a config.Rule with a compiled regex for testing.
// It uses reflection-free approach: Load() compiles regexes, but for tests
// we build rules via a helper that sets exported fields and injects the
// compiled pattern through a test-only shim.
func makeRule(name, pattern string, events, fieldPaths []string) config.Rule {
	// config.Rule.compiled is unexported; we route through config.Load() semantics
	// by embedding a rule into a minimal Config and loading it in-memory.
	_ = name
	_ = pattern
	_ = events
	_ = fieldPaths
	return config.Rule{} // placeholder — see loadRule below
}

// loadRule builds a single compiled Rule without touching the filesystem.
func loadRule(t *testing.T, name, pattern string, events, fieldPaths []string, message string) config.Rule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern %q: %v", pattern, err)
	}
	return config.NewRule(name, pattern, re, events, fieldPaths, "block", message)
}

// redirectionRule returns the canonical no-shell-redirection rule used in production.
func redirectionRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-shell-redirection",
		`(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)`,
		[]string{"PreToolUse", "beforeShellExecution"},
		[]string{"tool_input.command", "command"},
		"Shell redirection is not permitted.",
	)
}

// TestEvaluate_RedirectionBlocked verifies that common redirect patterns are blocked.
func TestEvaluate_RedirectionBlocked(t *testing.T) {
	rule := redirectionRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "claude stderr-to-stdout",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls 2>&1"},
			},
		},
		{
			name:  "claude discard stdout",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "make build >/dev/null"},
			},
		},
		{
			name:  "claude discard stderr",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test ./... 2>/dev/null"},
			},
		},
		{
			name:  "cursor shell execution fd redirect",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "cat file.txt 2>&1",
			},
		},
		{
			name:  "cursor combined redirect",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "./script.sh &>/dev/null",
			},
		},
		{
			name:  "cursor pipe with stderr",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "find . |& grep foo",
			},
		},
		{
			name:  "append redirect to null",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "cmd 2>>/dev/null"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(tc.event, tc.payload, []config.Rule{rule})
			if v == nil {
				t.Errorf("expected violation for command %q, got nil", tc.payload)
			}
		})
	}
}

// TestEvaluate_CleanCommandAllowed verifies that normal commands pass through.
func TestEvaluate_CleanCommandAllowed(t *testing.T) {
	rule := redirectionRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "simple build",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go build ./..."},
			},
		},
		{
			name:  "pipe without redirect",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls | grep foo"},
			},
		},
		{
			name:  "write to explicit file (allowed)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo hello > /tmp/out.txt"},
			},
		},
		{
			name:  "cursor clean command",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "git status",
			},
		},
		{
			name:  "non-matching event with redirect in payload",
			event: "SessionStart",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "ls 2>/dev/null"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(tc.event, tc.payload, []config.Rule{rule})
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

// TestEvaluate_EventFilter verifies rules only fire for their configured events.
func TestEvaluate_EventFilter(t *testing.T) {
	rule := redirectionRule(t)

	// A redirect in a PostToolUse payload should not fire the rule
	// because PostToolUse is not in the rule's events list.
	v := rules.Evaluate("PostToolUse", map[string]any{
		"tool_input": map[string]any{"command": "ls 2>/dev/null"},
	}, []config.Rule{rule})

	if v != nil {
		t.Errorf("rule fired for non-matching event PostToolUse, expected nil")
	}
}

// TestEvaluate_EmptyEventList verifies that an empty events list matches all events.
func TestEvaluate_EmptyEventList(t *testing.T) {
	rule := loadRule(t, "catch-all", `forbidden`, nil,
		[]string{"command"}, "forbidden keyword")

	v := rules.Evaluate("AnyEvent", map[string]any{
		"command": "do something forbidden here",
	}, []config.Rule{rule})

	if v == nil {
		t.Error("expected violation for catch-all rule, got nil")
	}
}

// TestEvaluate_DotPathExtraction verifies nested field access works correctly.
func TestEvaluate_DotPathExtraction(t *testing.T) {
	rule := loadRule(t, "deep-path", `secret`,
		[]string{"PreToolUse"},
		[]string{"a.b.c"},
		"found it",
	)

	// Deeply nested match.
	v := rules.Evaluate("PreToolUse", map[string]any{
		"a": map[string]any{
			"b": map[string]any{
				"c": "contains secret value",
			},
		},
	}, []config.Rule{rule})
	if v == nil {
		t.Error("expected violation for deeply nested field, got nil")
	}

	// Field missing entirely — should not fire.
	v = rules.Evaluate("PreToolUse", map[string]any{
		"a": map[string]any{"b": map[string]any{}},
	}, []config.Rule{rule})
	if v != nil {
		t.Errorf("expected nil for missing field, got violation %q", v.RuleName)
	}
}

// TestCheckedRuleNames verifies correct rule name reporting for a given event.
func TestCheckedRuleNames(t *testing.T) {
	rules1 := redirectionRule(t)
	allEvents := loadRule(t, "global", `x`, nil, []string{"command"}, "msg")

	rulesSlice := []config.Rule{rules1, allEvents}

	// PreToolUse matches both rules.
	names := rules.CheckedRuleNames("PreToolUse", rulesSlice)
	if len(names) != 2 {
		t.Errorf("expected 2 checked rules for PreToolUse, got %d: %v", len(names), names)
	}

	// Stop matches only the catch-all.
	names = rules.CheckedRuleNames("Stop", rulesSlice)
	if len(names) != 1 || names[0] != "global" {
		t.Errorf("expected [global] for Stop, got %v", names)
	}
}
