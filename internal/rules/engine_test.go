package rules_test

import (
	"regexp"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// loadRule builds a single compiled Rule without touching the filesystem.
func loadRule(t *testing.T, name, pattern string, events, fieldPaths []string, message string) config.Rule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern %q: %v", pattern, err)
	}
	return config.NewSimpleRule(name, pattern, re, events, fieldPaths, "block", message)
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

// loadConditionRule builds the no-git-write-from-home-cwd rule used in production.
func loadConditionRule(t *testing.T) config.Rule {
	t.Helper()
	cwdCond, err := config.NewCondition(
		[]string{"cwd"},
		`^/Users/agoodkind$`,
		"",
	)
	if err != nil {
		t.Fatalf("compile cwd condition: %v", err)
	}
	cmdCond, err := config.NewCondition(
		[]string{"tool_input.command", "command"},
		`^git\s+(add|commit|push|reset|rm|mv|rebase|merge|stash|clean|restore|switch|tag)`,
		`\s-C\s`,
	)
	if err != nil {
		t.Fatalf("compile cmd condition: %v", err)
	}
	return config.Rule{
		Name:             "no-git-write-from-home-cwd",
		Events:           []string{"PreToolUse", "preToolUse"},
		Conditions:       []config.Condition{cwdCond, cmdCond},
		Action:           "block",
		ViolationMessage: "git write operations are not permitted from the home directory.",
	}
}

func TestEvaluate_MultiCondition_HomeCWD(t *testing.T) {
	rule := loadConditionRule(t)

	homePayload := func(cmd string) map[string]any {
		return map[string]any{
			"cwd":        "/Users/agoodkind",
			"tool_input": map[string]any{"command": cmd},
		}
	}

	blocked := []string{
		"git add .",
		"git commit -m 'oops'",
		"git push origin main",
		"git reset --hard HEAD",
	}
	for _, cmd := range blocked {
		t.Run("blocked/"+cmd, func(t *testing.T) {
			v := rules.Evaluate("PreToolUse", homePayload(cmd), []config.Rule{rule})
			if v == nil {
				t.Errorf("expected block for %q in home cwd, got nil", cmd)
			}
		})
	}

	allowed := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "git with -C flag escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git -C /Users/agoodkind/Sites/proj commit -m msg"}},
		},
		{
			name:    "cwd is subdir not home",
			payload: map[string]any{"cwd": "/Users/agoodkind/Sites/proj", "tool_input": map[string]any{"command": "git commit -m msg"}},
		},
		{
			name:    "read-only git op from home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git status"}},
		},
		{
			name:    "non-git command from home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "ls -la"}},
		},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate("PreToolUse", tc.payload, []config.Rule{rule})
			if v != nil {
				t.Errorf("expected allow, got block: %s", v.Message)
			}
		})
	}
}
