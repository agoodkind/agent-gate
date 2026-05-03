package rules_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
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
	re, err := regex.Compile(pattern)
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

	// Field missing entirely should not fire.
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
	t.Setenv("HOME", "/Users/agoodkind")
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
	t.Setenv("HOME", "/Users/agoodkind")
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

// emdashDashPattern is the Unicode dash class used by no-emdashes rules in these tests.
const (
	emdashDashPattern        = `[\x{2010}-\x{2015}\x{2212}\x{2E3A}\x{2E3B}\x{FE31}\x{FE32}\x{FE58}\x{FE63}\x{FF0D}]`
	doubleHyphenProsePattern = `(?m)(?|(?:` + "`" + `[^` + "`" + `\n]+` + "`" + `|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*)\s+(--)(?=\s+[A-Za-z][A-Za-z0-9_./-]*)|\b(?!(?:bash|sh|zsh|fish|exec|command|env|xargs|sudo|doas|go|git|make|npm|pnpm|yarn|node|python|python3|ruby|perl|cargo|docker|kubectl|helm|terraform|ansible|rg|grep|sed|awk|jq|curl|ssh|scp|rsync)\b)[A-Za-z][A-Za-z0-9_./-]*(--)(?=[A-Za-z][A-Za-z0-9_./-]*))`
)

func TestEmdashDashPatternMatchesU2011(t *testing.T) {
	t.Helper()
	re := regex.MustCompile(emdashDashPattern)
	if !re.MatchString("non\u2011breaking") {
		t.Fatal("emdashDashPattern should match U+2011 (non-breaking hyphen)")
	}
}

// emdashMainRule returns the broad no-emdashes rule (tool_input.prompt excluded; see emdashPromptUnlessTaskRule).
func emdashMainRule(t *testing.T) config.Rule {
	t.Helper()
	return loadRule(t,
		"no-emdashes",
		emdashDashPattern,
		[]string{"PreToolUse", "preToolUse", "beforeShellExecution", "Stop", "SubagentStop", "afterAgentResponse"},
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.command", "tool_input.description", "command", "assistant_message", "last_assistant_message", "text"},
		"Unicode dashes are not permitted.",
	)
}

// emdashPromptUnlessTaskRule blocks typographic dashes in tool_input.prompt unless tool_name is Task (sub-agent).
func emdashPromptUnlessTaskRule(t *testing.T) config.Rule {
	t.Helper()
	promptCond, err := config.NewCondition(
		[]string{"tool_input.prompt"},
		emdashDashPattern,
		"",
	)
	if err != nil {
		t.Fatalf("compile prompt condition: %v", err)
	}
	toolCond, err := config.NewCondition(
		[]string{"tool_name"},
		"",
		`(?i)^task$`,
	)
	if err != nil {
		t.Fatalf("compile tool_name condition: %v", err)
	}
	return config.Rule{
		Name:             "no-emdashes-tool-input-prompt-unless-subagent-task",
		ClaudeEvents:     []string{"PreToolUse"},
		CursorEvents:     []string{"preToolUse"},
		Conditions:       []config.Condition{promptCond, toolCond},
		Action:           "block",
		ViolationMessage: "Unicode dashes are not permitted in tool_input.prompt.",
	}
}

// emdashRules returns the main no-emdashes rule plus the conditional prompt rule (production order).
func emdashRules(t *testing.T) []config.Rule {
	t.Helper()
	return []config.Rule{emdashMainRule(t), emdashPromptUnlessTaskRule(t)}
}

// TestEvaluate_EmdashBlocked verifies that Unicode dash variants are blocked.
func TestEvaluate_EmdashBlocked(t *testing.T) {
	rulesSlice := emdashRules(t)

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
		{
			name:  "em dash in tool_input.prompt with non-Task tool_name",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "Write",
				"tool_input": map[string]any{
					"prompt": "Instructions \u2014 with a dash.",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, rulesSlice)
			if v == nil {
				t.Error("expected violation, got nil")
			}
		})
	}
}

// TestEvaluate_EmdashAllowed verifies that regular hyphens and clean text pass through.
func TestEvaluate_EmdashAllowed(t *testing.T) {
	rulesSlice := emdashRules(t)

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
			name:    "empty payload",
			event:   "PreToolUse",
			payload: map[string]any{},
		},
		{
			name:  "em dash in Task tool prompt (Claude PreToolUse)",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "Task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
		{
			name:  "em dash in Task tool prompt case-insensitive tool_name",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_name": "task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
		{
			name:  "em dash in Task tool prompt (Cursor preToolUse)",
			event: "preToolUse",
			payload: map[string]any{
				"tool_name": "Task",
				"tool_input": map[string]any{
					"prompt": "Sub-agent brief \u2014 with a dash.",
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, rulesSlice)
			if v != nil {
				t.Errorf("expected no violation, got rule %q: %s", v.RuleName, v.Message)
			}
		})
	}
}

func doubleHyphenProseRule(t *testing.T) config.Rule {
	t.Helper()
	rule := loadRule(t,
		"no-double-hyphen-prose",
		doubleHyphenProsePattern,
		[]string{"PreToolUse", "preToolUse", "Stop", "afterAgentResponse"},
		[]string{"tool_input.content", "tool_input.new_string", "tool_input.description", "edits[*].new_string", "assistant_message", "last_assistant_message", "text"},
		"ASCII double-hyphen is not permitted as a prose dash.",
	)
	rule.DiagnosticGroup = 1
	return rule
}

func TestEvaluateAllUsesDiagnosticGroupSpan(t *testing.T) {
	rule := loadRule(t,
		"capture-only",
		`prefix (bad) suffix`,
		[]string{"Stop"},
		[]string{"assistant_message"},
		"capture is blocked.",
	)
	rule.DiagnosticGroup = 1
	value := "prefix bad suffix"
	payload := map[string]any{
		"assistant_message": value,
	}

	got := rules.EvaluateAll("claude", "Stop", payload, []config.Rule{rule})
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, "bad")
	wantEnd := wantStart + len("bad")
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluateAllUsesConditionDiagnosticGroupSpan(t *testing.T) {
	condition, err := config.NewCondition(
		[]string{"assistant_message"},
		`prefix (bad) suffix`,
		"",
	)
	if err != nil {
		t.Fatalf("compile condition: %v", err)
	}
	condition.DiagnosticGroup = 1
	rule := config.Rule{
		Name:             "condition-capture-only",
		Events:           []string{"Stop"},
		Conditions:       []config.Condition{condition},
		Action:           "block",
		ViolationMessage: "capture is blocked.",
	}
	value := "prefix bad suffix"
	payload := map[string]any{
		"assistant_message": value,
	}

	got := rules.EvaluateAll("claude", "Stop", payload, []config.Rule{rule})
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, "bad")
	wantEnd := wantStart + len("bad")
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluate_DoubleHyphenProseBlocked(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "spaced lazy em dash",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Word -- word"},
			},
		},
		{
			name:  "unspaced lazy em dash",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Word--word"},
			},
		},
		{
			name:  "backticked command label in prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "`mwan cutover` -- deprecated migration/rollback tooling"},
			},
		},
		{
			name:  "assistant response prose",
			event: "Stop",
			payload: map[string]any{
				"assistant_message": "This works -- but it should be rewritten.",
			},
		},
		{
			name:  "cursor edit array",
			event: "afterAgentResponse",
			payload: map[string]any{
				"edits": []any{
					map[string]any{"new_string": "Clean"},
					map[string]any{"new_string": "Old text -- new text"},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
			if v == nil {
				t.Fatal("expected violation, got nil")
			}
		})
	}
}

func TestEvaluate_DoubleHyphenProseMatchSpan(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	value := "// allocator is only used for temporary allocations -- all memory"
	payload := map[string]any{
		"tool_input": map[string]any{"content": value},
	}

	got := rules.EvaluateAll("claude", "PreToolUse", payload, []config.Rule{rule})
	if len(got) != 1 {
		t.Fatalf("EvaluateAll returned %d matches, want 1: %#v", len(got), got)
	}

	wantStart := strings.Index(value, "--")
	wantEnd := wantStart + len("--")
	if got[0].Start != wantStart || got[0].End != wantEnd {
		t.Fatalf("match span = [%d,%d), want [%d,%d)", got[0].Start, got[0].End, wantStart, wantEnd)
	}
}

func TestEvaluate_DoubleHyphenProseAllowed(t *testing.T) {
	rule := doubleHyphenProseRule(t)
	cases := []struct {
		name    string
		event   string
		payload map[string]any
	}{
		{
			name:  "command field with flags is ignored",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"command": "go test --count=1 ./..."},
			},
		},
		{
			name:  "bare flag in prose field",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Use --count=1 for this test."},
			},
		},
		{
			name:  "exec option separator in prose field",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Example: exec -- input"},
			},
		},
		{
			name:  "regular hyphenated prose",
			event: "PreToolUse",
			payload: map[string]any{
				"tool_input": map[string]any{"new_string": "Use well-formed words and kebab-case identifiers."},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(systemFor(tc.event), tc.event, tc.payload, []config.Rule{rule})
			if v != nil {
				t.Fatalf("expected allow, got %s", v.RuleName)
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

func makeGoThroughMakeRule() config.Rule {
	return config.Rule{
		Name:         "go-build-test-through-make",
		ClaudeEvents: []string{"PreToolUse"},
		CursorEvents: []string{"preToolUse", "beforeShellExecution"},
		CodexEvents:  []string{"PreToolUse"},
		GeminiEvents: []string{"BeforeTool"},
		Conditions: []config.Condition{
			{
				Kind:        "command",
				Argv0:       "go",
				Subcommands: []string{"build", "test"},
				StripEnv:    true,
				StripArgs:   []string{"env", "time", "command"},
				CwdFlags:    []string{"-C"},
				Pattern:     `^(?:build(?:\s|$)|test(?:\s+.*)?\s(?:\./\.\.\.|all)(?:\s|$))`,
			},
			{
				Kind:        "project",
				RootMarkers: []string{"go.mod"},
				RequireAny:  []string{"Makefile", "makefile", "GNUmakefile"},
			},
		},
		Action:           "block",
		ViolationMessage: "Use make.",
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestEvaluate_CommandAndProjectConditions_GoThroughMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	outside := t.TempDir()
	subdir := filepath.Join(root, "cmd", "server")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir subdir: %v", err)
	}

	rule := makeGoThroughMakeRule()
	cases := []struct {
		name    string
		system  string
		event   string
		payload map[string]any
		want    bool
	}{
		{
			name:   "claude go build in module with makefile",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go build ./..."},
			},
			want: true,
		},
		{
			name:   "codex env-prefixed go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "CGO_ENABLED=1 go test ./..."},
			},
			want: true,
		},
		{
			name:   "operation workdir beats chat cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":           outside,
				"effective_cwd": root,
				"tool_input":    map[string]any{"command": "go test ./..."},
			},
			want: true,
		},
		{
			name:   "tool input workdir beats chat cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd": outside,
				"tool_input": map[string]any{
					"command": "go test ./...",
					"workdir": root,
				},
			},
			want: true,
		},
		{
			name:   "env wrapper go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "env CGO_ENABLED=1 go test ./..."},
			},
			want: true,
		},
		{
			name:   "time wrapper go test in module with makefile",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "time go test ./..."},
			},
			want: true,
		},
		{
			name:   "go -C uses command-specific cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go -C " + root + " test ./..."},
			},
			want: true,
		},
		{
			name:   "go -C equals uses command-specific cwd",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go -C=" + root + " test ./..."},
			},
			want: true,
		},
		{
			name:   "cursor command field in module subdir",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"cwd":     subdir,
				"command": "/opt/homebrew/bin/go test ./...",
			},
			want: true,
		},
		{
			name:   "cd into module before go test",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "cd " + root + " && go test ./..."},
			},
			want: true,
		},
		{
			name:   "go test before cd into module uses original cwd",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        outside,
				"tool_input": map[string]any{"command": "go test ./... && cd " + root},
			},
			want: false,
		},
		{
			name:   "heredoc content with go build is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "cat <<'EOF' > Makefile\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\nEOF"},
			},
			want: false,
		},
		{
			name:   "heredoc with later direct go test still blocks",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "cat <<'EOF' > Makefile\nbuild:\n\tgo build ./...\nEOF\ngo test ./..."},
			},
			want: true,
		},
		{
			name:   "make test is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "make test"},
			},
			want: false,
		},
		{
			name:   "go list is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go list ./..."},
			},
			want: false,
		},
		{
			name:   "targeted go test package is allowed",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test ./internal/rules"},
			},
			want: false,
		},
		{
			name:   "targeted go test with run filter is allowed",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test ./internal/rules -run TestEvaluate_CommandAndProjectConditions_GoThroughMake"},
			},
			want: false,
		},
		{
			name:   "current package go test is allowed",
			system: "cursor",
			event:  "beforeShellExecution",
			payload: map[string]any{
				"cwd":     subdir,
				"command": "go test",
			},
			want: false,
		},
		{
			name:   "go test all is blocked",
			system: "claude",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test all"},
			},
			want: true,
		},
		{
			name:   "go test ellipsis with flags is blocked",
			system: "codex",
			event:  "PreToolUse",
			payload: map[string]any{
				"cwd":        root,
				"tool_input": map[string]any{"command": "go test -count=1 -run TestThing ./..."},
			},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := rules.Evaluate(tc.system, tc.event, tc.payload, []config.Rule{rule})
			if got := v != nil; got != tc.want {
				t.Fatalf("blocked = %v, want %v; violation = %#v", got, tc.want, v)
			}
		})
	}
}

func TestEvaluate_CommandAndProjectConditions_OrderIndependent(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	rule := makeGoThroughMakeRule()
	rule.Conditions[0], rule.Conditions[1] = rule.Conditions[1], rule.Conditions[0]

	v := rules.Evaluate("claude", "PreToolUse", map[string]any{
		"cwd":        t.TempDir(),
		"tool_input": map[string]any{"command": "cd " + root + " && go test ./..."},
	}, []config.Rule{rule})

	if v == nil {
		t.Fatal("expected project condition to use matched command cwd regardless of condition order")
	}
}

func TestEvaluate_ProjectCondition_AllowsWhenProjectDoesNotSupportMake(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.test/project\n")

	v := rules.Evaluate("claude", "PreToolUse", map[string]any{
		"cwd":        root,
		"tool_input": map[string]any{"command": "go test ./..."},
	}, []config.Rule{makeGoThroughMakeRule()})

	if v != nil {
		t.Fatalf("expected allow without Makefile, got %s", v.Message)
	}
}

func TestEvaluate_ProjectCondition_AllowsOutsideMarkedProject(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "Makefile"), "test:\n\tgo test ./...\n")

	v := rules.Evaluate("claude", "PreToolUse", map[string]any{
		"cwd":        root,
		"tool_input": map[string]any{"command": "go test ./..."},
	}, []config.Rule{makeGoThroughMakeRule()})

	if v != nil {
		t.Fatalf("expected allow outside Go module, got %s", v.Message)
	}
}
