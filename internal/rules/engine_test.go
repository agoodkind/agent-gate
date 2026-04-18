package rules_test

import (
	"regexp"
	"testing"
	"unicode"
	"unicode/utf8"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

// systemFor infers "claude" or "cursor" from a PascalCase/camelCase event name.
func systemFor(event string) string {
	r, _ := utf8.DecodeRuneInString(event)
	if unicode.IsUpper(r) {
		return "claude"
	}
	return "cursor"
}

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
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
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
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
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
	v := rules.Evaluate("claude", "PostToolUse", map[string]any{
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

	v := rules.Evaluate("unknown", "AnyEvent", map[string]any{
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
	v := rules.Evaluate("claude", "PreToolUse", map[string]any{
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
	v = rules.Evaluate("claude", "PreToolUse", map[string]any{
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
	names := rules.CheckedRuleNames("claude", "PreToolUse", rulesSlice)
	if len(names) != 2 {
		t.Errorf("expected 2 checked rules for PreToolUse, got %d: %v", len(names), names)
	}

	// Stop matches only the catch-all.
	names = rules.CheckedRuleNames("claude", "Stop", rulesSlice)
	if len(names) != 1 || names[0] != "global" {
		t.Errorf("expected [global] for Stop, got %v", names)
	}
}

// loadConditionRule builds the no-git-write-from-home-cwd rule used in production.
// loadConditionRule builds the no-git-write-from-home-cwd rule used in production.
// Uses "effective_cwd" so that cd chains are simulated before matching.
func loadConditionRule(t *testing.T) config.Rule {
	t.Helper()
	cwdCond, err := config.NewCondition(
		[]string{"effective_cwd"},
		`^/Users/agoodkind$`,
		"",
	)
	if err != nil {
		t.Fatalf("compile effective_cwd condition: %v", err)
	}
	cmdCond, err := config.NewCondition(
		[]string{"cmd_segments"},
		`(?m)^git\s+(add|commit|push|reset|rm|mv|rebase|merge|stash|clean|restore|switch|tag)`,
		`(\s-C\s[/~.]|\s--git-dir=|\s--work-tree=)`,
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
		// Compound: first op is read-only but second is a write op from home.
		"git status && git commit -m msg",
	}
	for _, cmd := range blocked {
		t.Run("blocked/"+cmd, func(t *testing.T) {
			v := rules.Evaluate("claude", "PreToolUse", homePayload(cmd), []config.Rule{rule})
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
			name:    "git with --git-dir escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git --git-dir=/Users/agoodkind/Sites/proj/.git commit -m msg"}},
		},
		{
			name:    "git with --work-tree escapes home",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "git --work-tree=/Users/agoodkind/Sites/proj commit -m msg"}},
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
		// Regression: git subcommand appearing inside an argument must not be matched.
		{
			name:    "git log with grep for commit keyword",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": `git log --grep="git commit"`}},
		},
		{
			name:    "cd to project subdir then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /Users/agoodkind/Sites/proj && git commit -m msg"}},
		},
		{
			name:    "cd tilde-path then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~/Sites/proj && git commit -m msg"}},
		},
		{
			name:    "cd absolute non-home then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /tmp/work && git commit -m msg"}},
		},
		{
			name:    "ls then cd to project then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "ls && cd ~/Sites/proj && git commit -m msg"}},
		},
	}
	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate("claude", "PreToolUse", tc.payload, []config.Rule{rule})
			if v != nil {
				t.Errorf("expected allow, got block: %s", v.Message)
			}
		})
	}
}

// TestEvaluate_EffectiveCwd_StillHome verifies that cd back to home is still blocked.
func TestEvaluate_EffectiveCwd_StillHome(t *testing.T) {
	rule := loadConditionRule(t)

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{
			name:    "cd to home explicitly then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~ && git commit -m msg"}},
		},
		{
			name:    "cd to home absolute then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd /Users/agoodkind && git commit -m msg"}},
		},
		{
			name:    "cd to project then cd back home then commit",
			payload: map[string]any{"cwd": "/Users/agoodkind", "tool_input": map[string]any{"command": "cd ~/Sites/proj && cd ~ && git commit -m msg"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate("claude", "PreToolUse", tc.payload, []config.Rule{rule})
			if v == nil {
				t.Errorf("expected block (effective cwd is still home), got nil")
			}
		})
	}
}

// TestApplyCdChain directly tests the cd simulation logic.
func TestApplyCdChain(t *testing.T) {
	home := "/Users/agoodkind"
	start := "/Users/agoodkind"

	cases := []struct {
		command string
		want    string
	}{
		{"git commit", "/Users/agoodkind"},
		{"cd /tmp && git commit", "/tmp"},
		{"cd ~/Sites/proj && git commit", "/Users/agoodkind/Sites/proj"},
		{"cd ~ && git commit", "/Users/agoodkind"},
		{"cd /tmp && cd ~/Sites && git commit", "/Users/agoodkind/Sites"},
		{"ls && cd ~/Sites && git commit", "/Users/agoodkind/Sites"},
		{"cd \"/Users/agoodkind/Sites/my proj\" && git commit", "/Users/agoodkind/Sites/my proj"},
		{"cd '../sibling'", "/Users/sibling"},
	}

	for _, tc := range cases {
		t.Run(tc.command, func(t *testing.T) {
			got := rules.ApplyCdChain(start, home, tc.command)
			if got != tc.want {
				t.Errorf("ApplyCdChain(%q) = %q, want %q", tc.command, got, tc.want)
			}
		})
	}
}

// emdashRule returns the canonical no-emdashes rule used in production.
func emdashRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-emdashes",
		`[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`,
		[]string{"PreToolUse", "preToolUse", "beforeShellExecution", "Stop", "SubagentStop", "afterAgentResponse"},
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.command", "tool_input.prompt", "tool_input.description", "command", "assistant_message", "last_assistant_message", "text"},
		"Unicode dashes are not permitted.",
	)
}

// TestEvaluate_EmdashBlocked verifies that Unicode dash variants are blocked.
func TestEvaluate_EmdashBlocked(t *testing.T) {
	rule := emdashRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "em dash in file content (Write tool)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "This is important \u2014 very important."},
			},
		},
		{
			name:  "en dash in edit replacement (Edit tool)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "pages 10\u201320"},
			},
		},
		{
			name:  "figure dash in command",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "echo 'value\u2012dash'"},
			},
		},
		{
			name:  "non-breaking hyphen in prompt",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"prompt": "non\u2011breaking text"},
			},
		},
		{
			name:  "horizontal bar in description",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"description": "separator\u2015here"},
			},
		},
		{
			name:  "unicode hyphen U+2010",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "soft\u2010hyphen"},
			},
		},
		{
			name:  "cursor shell command with em dash",
			event: "beforeShellExecution",
			payload: map[string]any{
				"command": "echo 'text \u2014 more text'",
			},
		},
		{
			name:  "cursor preToolUse with en dash",
			event: "preToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "2020\u20132025"},
			},
		},
		{
			name:  "em dash in assistant_message (Stop)",
			event: "Stop",
			payload: map[string]any{
				"assistant_message": "Here is the result \u2014 it works.",
			},
		},
		{
			name:  "en dash in last_assistant_message (SubagentStop)",
			event: "SubagentStop",
			payload: map[string]any{
				"last_assistant_message": "Pages 10\u201320 are relevant.",
			},
		},
		{
			name:  "em dash in Cursor afterAgentResponse text",
			event: "afterAgentResponse",
			payload: map[string]any{
				"text": "The result \u2014 as expected \u2014 is correct.",
			},
		},
		{
			name:  "minus sign U+2212",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "5 \u2212 3 = 2"},
			},
		},
		{
			name:  "two-em dash U+2E3A",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "word\u2E3Abreak"},
			},
		},
		{
			name:  "three-em dash U+2E3B",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "word\u2E3Bbreak"},
			},
		},
		{
			name:  "fullwidth hyphen-minus U+FF0D",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "full\uFF0Dwidth"},
			},
		},
		{
			name:  "small em dash U+FE58",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "small\uFE58dash"},
			},
		},
		{
			name:  "vertical em dash U+FE31",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "vert\uFE31dash"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
			if v == nil {
				t.Error("expected violation, got nil")
			}
		})
	}
}

// TestEvaluate_EmdashAllowed verifies that regular hyphens and clean text pass through.
func TestEvaluate_EmdashAllowed(t *testing.T) {
	rule := emdashRule(t)

	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "kebab-case filename",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "my-component-file.tsx"},
			},
		},
		{
			name:  "CLI flags with hyphens",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test --count=1 --race ./..."},
			},
		},
		{
			name:  "plain text no dashes",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "This is a normal sentence with no special dashes."},
			},
		},
		{
			name:  "hyphen-minus in code",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "x := a - b"},
			},
		},
		{
			name:  "non-matching event",
			event: "PostToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "has em dash \u2014 here"},
			},
		},
		{
			name:  "empty payload",
			event: "PreToolUse",
			payload: map[string]any{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

// arrayPathRule returns a rule that matches em dashes in edits[*].new_string.
func arrayPathRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-emdashes-in-edits",
		`[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`,
		[]string{"afterFileEdit"},
		[]string{"edits[*].new_string"},
		"Unicode dashes are not permitted in edits.",
	)
}

// TestNavigatePath_ArrayWildcard verifies [*] path traversal through arrays.
func TestNavigatePath_ArrayWildcard(t *testing.T) {
	rule := arrayPathRule(t)

	blocked := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "em dash in first edit",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"old_string": "before", "new_string": "after \u2014 done"},
				},
			},
		},
		{
			name: "en dash in second edit",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"old_string": "a", "new_string": "clean"},
					map[string]any{"old_string": "b", "new_string": "pages 10\u201320"},
				},
			},
		},
		{
			name: "unicode hyphen in one of three edits",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "normal text"},
					map[string]any{"new_string": "soft\u2010hyphen here"},
					map[string]any{"new_string": "also clean"},
				},
			},
		},
	}

	for _, tc := range blocked {
		t.Run("blocked/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate("cursor", "afterFileEdit", tc.payload, []config.Rule{rule})
			if v == nil {
				t.Error("expected violation, got nil")
			}
		})
	}

	allowed := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "all edits are clean",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "clean text"},
					map[string]any{"new_string": "also-clean-with-hyphen"},
				},
			},
		},
		{
			name: "empty edits array",
			payload: map[string]any{
				"edits": []any{},
			},
		},
		{
			name: "no edits key",
			payload: map[string]any{
				"tool_input": map[string]any{"content": "clean"},
			},
		},
	}

	for _, tc := range allowed {
		t.Run("allowed/"+tc.name, func(t *testing.T) {
			v := rules.Evaluate("cursor", "afterFileEdit", tc.payload, []config.Rule{rule})
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

// TestCmdSegments verifies the cmd_segments virtual field splits correctly.
func TestCmdSegments(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]any
		want    string
	}{
		{
			name:    "single command",
			payload: map[string]any{"tool_input": map[string]any{"command": "git commit -m msg"}},
			want:    "git commit -m msg",
		},
		{
			name:    "and-and chain",
			payload: map[string]any{"tool_input": map[string]any{"command": "git status && git commit -m msg"}},
			want:    "git status\ngit commit -m msg",
		},
		{
			name:    "semicolon chain",
			payload: map[string]any{"tool_input": map[string]any{"command": "cd /tmp; git commit -m msg"}},
			want:    "cd /tmp\ngit commit -m msg",
		},
		{
			name:    "argument with keyword inside does not split",
			payload: map[string]any{"tool_input": map[string]any{"command": `git log --grep="git commit"`}},
			want:    `git log --grep="git commit"`,
		},
		{
			name:    "no command field returns empty",
			payload: map[string]any{},
			want:    "",
		},
		{
			name:    "cursor-style command field",
			payload: map[string]any{"command": "ls && git push"},
			want:    "ls\ngit push",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rules.CmdSegments(tc.payload)
			if got != tc.want {
				t.Errorf("CmdSegments() = %q, want %q", got, tc.want)
			}
		})
	}
}
