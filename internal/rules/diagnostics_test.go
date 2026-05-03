package rules_test

import (
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
	payload := map[string]any{
		"assistant_message": "alpha xx\nrun tests >/dev/null\nx marks",
	}

	got := rules.EvaluateAll("codex", "Stop", payload, []config.Rule{ruleA, ruleB})
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
		"1 | alpha xx",
		"  |       ^A",
		"2 | run tests >/dev/null",
		"  |           ^B-------",
		"A = no-x",
		"message: letter x is blocked.",
		"file: /tmp/example.txt",
		"line: 3",
		"column: 1",
		"B = no-dev-null",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
}

func TestFormatViolationsAlignsDoubleHyphenSpan(t *testing.T) {
	value := "// allocator is only used for temporary allocations -- all memory"
	start := strings.Index(value, "--")
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
			End:       start + len("--"),
		},
	})
	wantMarker := "  | " + strings.Repeat(" ", start) + "^A"
	for _, want := range []string{
		"1 | " + value,
		wantMarker,
		"column: 53",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "^A-") {
		t.Fatalf("diagnostic marker extended past the two-character span:\n%s", got)
	}
}
