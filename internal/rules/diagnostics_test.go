package rules_test

import (
	"context"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
)

func TestEvaluateAllCollectsConcreteMatches(t *testing.T) {
	ruleA := loadRule(t,
		"no-x",
		`x+`,
		[]string{"Stop"},
		[]string{"assistant_message"},
		"letter x is blocked.",
	)
	ruleB := loadRule(t,
		"no-dev-null",
		`>/dev/null`,
		[]string{"Stop"},
		[]string{"assistant_message"},
		"dev null redirection is blocked.",
	)
	payload := rules.FieldSet{
		AssistantMessage: "alpha xx\nrun tests >/dev/null\nx marks",
	}

	got := rules.EvaluateAll(context.Background(), "codex", "Stop", payload, []config.Rule{ruleA, ruleB}, nil)
	if len(got) != 3 {
		t.Fatalf("EvaluateAll returned %d matches, want 3: %#v", len(got), got)
	}
	if got[0].RuleName != "no-x" || got[0].Start != 6 || got[0].End != 8 {
		t.Fatalf("first match = %#v", got[0])
	}
	if got[1].RuleName != "no-x" || got[2].RuleName != "no-dev-null" {
		t.Fatalf("unexpected rule order: %#v", got)
	}
}

func TestFormatViolationsLineNumberedLegend(t *testing.T) {
	value := "alpha xx\nrun tests >/dev/null\nx marks"
	violations := []rules.MatchViolation{
		{
			RuleName:  "no-x",
			Message:   "letter x is blocked.",
			FieldPath: "assistant_message",
			Value:     value,
			Start:     6,
			End:       8,
		},
		{
			RuleName:  "no-dev-null",
			Message:   "dev null redirection is blocked.",
			FieldPath: "assistant_message",
			Value:     value,
			Start:     19,
			End:       29,
		},
		{
			RuleName:  "no-x",
			Message:   "letter x is blocked.",
			FieldPath: "assistant_message",
			FilePath:  "/tmp/example.txt",
			Value:     value,
			Start:     30,
			End:       31,
		},
	}

	got := rules.FormatViolations(violations)
	for _, want := range []string{
		"agent-gate blocked 3 violations:",
		"Rules:\n- A = no-x\n  message: letter x is blocked.",
		"- B = no-dev-null\n  message: dev null redirection is blocked.",
		"Matches:\n- field: assistant_message",
		"  - rule: A\n    line: 1\n    column: 7\n    match: \"xx\"\n    text: \"alpha xx\"",
		"  - rule: B\n    line: 2\n    column: 11\n    match: \">/dev/null\"\n    text: \"run tests >/dev/null\"",
		"  - rule: A\n    file: /tmp/example.txt\n    line: 3\n    column: 1\n    match: \"x\"\n    text: \"x marks\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
	for _, blocked := range []string{"```", "^A", "^B", "occurrences:"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("diagnostic contains width-dependent marker %q:\n%s", blocked, got)
		}
	}
}

func TestFormatViolationsReportsCaptureGroupSpan(t *testing.T) {
	value := "prefix bad suffix"
	start := strings.Index(value, "bad")
	if start < 0 {
		t.Fatal("test fixture is missing capture text")
	}

	got := rules.FormatViolations([]rules.MatchViolation{
		{
			RuleName:  "capture-only",
			Message:   "capture is blocked.",
			FieldPath: "assistant_message",
			Value:     value,
			Start:     start,
			End:       start + len("bad"),
		},
	})
	for _, want := range []string{
		"match: \"bad\"",
		"text: \"prefix bad suffix\"",
		"A = capture-only",
		"message: capture is blocked.",
		"line: 1",
		"column: 8",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
}

func TestFormatViolationsReportsDoubleHyphenSpan(t *testing.T) {
	doubleHyphen := strings.Repeat("-", 2)
	value := "// allocator is only used for temporary allocations " + doubleHyphen + " all memory"
	start := strings.Index(value, doubleHyphen)
	if start < 0 {
		t.Fatal("test fixture is missing double hyphen")
	}

	got := rules.FormatViolations([]rules.MatchViolation{
		{
			RuleName:  "no-double-hyphen-prose",
			Message:   "ASCII double-hyphen is not permitted as a prose dash.",
			FieldPath: "tool_input.content",
			Value:     value,
			Start:     start,
			End:       start + len(doubleHyphen),
		},
	})
	for _, want := range []string{
		"- field: tool_input.content",
		"match: \"--\"",
		"text: \"" + value + "\"",
		"A = no-double-hyphen-prose",
		"column: 53",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
}

func TestFormatViolationsQuotesBacktickRunsWithoutFence(t *testing.T) {
	value := "blocked ``` value"
	start := strings.Index(value, "```")
	if start < 0 {
		t.Fatal("test fixture is missing backtick run")
	}

	got := rules.FormatViolations([]rules.MatchViolation{
		{
			RuleName:  "no-backticks",
			Message:   "backticks are blocked.",
			FieldPath: "assistant_message",
			Value:     value,
			Start:     start,
			End:       start + len("```"),
		},
	})
	for _, want := range []string{
		"match: \"```\"",
		"text: \"blocked ``` value\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "```text") {
		t.Fatalf("diagnostic contains markdown fence:\n%s", got)
	}
}

func TestFormatViolationsQuotesUnicodeLiterally(t *testing.T) {
	dash := string(rune(0x2014))
	value := "emdash validation: " + dash
	start := strings.Index(value, dash)
	if start < 0 {
		t.Fatal("test fixture is missing unicode dash")
	}

	got := rules.FormatViolations([]rules.MatchViolation{
		{
			RuleName:  "no-emdashes",
			Message:   "typographic dash is blocked.",
			FieldPath: "tool_input.content",
			Value:     value,
			Start:     start,
			End:       start + len(dash),
		},
	})
	for _, want := range []string{
		`match: "` + dash + `"`,
		`text: "emdash validation: ` + dash + `"`,
		"column: 20",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
}
