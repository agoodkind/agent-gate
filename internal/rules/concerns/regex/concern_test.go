package regex_test

import (
	"context"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	intregex "goodkind.io/agent-gate/internal/regex"
	regexconcern "goodkind.io/agent-gate/internal/rules/concerns/regex"
)

// testFields is a minimal FieldAccessor for tests.
type testFields struct {
	values   map[string]string
	filePath string
}

func (f *testFields) String(selector config.FieldSelector) string {
	// Map the selector constant back to a string key for test lookup.
	// This covers the selectors used in the test rules below.
	key := selectorKey(selector)
	return f.values[key]
}

func (f *testFields) FilePathValue() string {
	return f.filePath
}

// selectorKey maps the small subset of selectors used in tests to string keys.
func selectorKey(sel config.FieldSelector) string {
	switch sel {
	case config.FieldToolInputContent:
		return "tool_input.content"
	case config.FieldToolName:
		return "tool_name"
	default:
		return ""
	}
}

func TestConcern_Execute_MatchesViolation(t *testing.T) {
	pattern := `AKIA[0-9A-Z]{16}`
	re, err := intregex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}

	selectors := config.CompileFieldSelectorSpecs([]string{"tool_input.content"})
	fields := &testFields{
		values: map[string]string{
			"tool_input.content": "Found key AKIAIOSFODNN7EXAMPLE in code.",
		},
	}

	in := regexconcern.Input{
		RuleName:        "no-aws-key",
		Selectors:       selectors,
		CompiledPattern: re,
		DiagnosticGroup: 0,
		Fields:          fields,
	}

	c := regexconcern.New("no-aws-key")
	raw, err := c.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	out, ok := raw.(regexconcern.Outcome)
	if !ok {
		t.Fatalf("Execute returned unexpected type %T", raw)
	}

	if len(out.Matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %#v", len(out.Matches), out.Matches)
	}

	m := out.Matches[0]
	if m.FieldPath != "tool_input.content" {
		t.Errorf("FieldPath = %q, want %q", m.FieldPath, "tool_input.content")
	}
	matchText := m.Value[m.Start:m.End]
	if matchText != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("matched text = %q, want %q", matchText, "AKIAIOSFODNN7EXAMPLE")
	}
}

func TestConcern_Execute_NoMatch(t *testing.T) {
	pattern := `AKIA[0-9A-Z]{16}`
	re, err := intregex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}

	selectors := config.CompileFieldSelectorSpecs([]string{"tool_input.content"})
	fields := &testFields{
		values: map[string]string{
			"tool_input.content": "This is a clean string with no secrets.",
		},
	}

	in := regexconcern.Input{
		RuleName:        "no-aws-key",
		Selectors:       selectors,
		CompiledPattern: re,
		DiagnosticGroup: 0,
		Fields:          fields,
	}

	c := regexconcern.New("no-aws-key")
	raw, err := c.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	out, ok := raw.(regexconcern.Outcome)
	if !ok {
		t.Fatalf("Execute returned unexpected type %T", raw)
	}

	if len(out.Matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(out.Matches))
	}
}

func TestConcern_Execute_WrongInputType(t *testing.T) {
	c := regexconcern.New("test-rule")
	raw, err := c.Execute(context.Background(), "not-an-Input")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	out, ok := raw.(regexconcern.Outcome)
	if !ok {
		t.Fatalf("returned %T, want Outcome", raw)
	}
	if len(out.Matches) != 0 {
		t.Errorf("expected empty matches for wrong input type, got %d", len(out.Matches))
	}
}

func TestConditionMatch_Passes(t *testing.T) {
	cond, err := config.NewCondition([]string{"tool_input.content"}, `secret`, "")
	if err != nil {
		t.Fatalf("NewCondition: %v", err)
	}
	fields := &testFields{
		values: map[string]string{
			"tool_input.content": "contains secret value",
		},
	}
	if !regexconcern.ConditionMatch(fields, &cond) {
		t.Error("expected ConditionMatch to return true for matching pattern")
	}
}

func TestConditionMatch_FailsOnPattern(t *testing.T) {
	cond, err := config.NewCondition([]string{"tool_input.content"}, `secret`, "")
	if err != nil {
		t.Fatalf("NewCondition: %v", err)
	}
	fields := &testFields{
		values: map[string]string{
			"tool_input.content": "clean value",
		},
	}
	if regexconcern.ConditionMatch(fields, &cond) {
		t.Error("expected ConditionMatch to return false when pattern does not match")
	}
}

func TestConditionMatch_FailsOnNotPattern(t *testing.T) {
	cond, err := config.NewCondition([]string{"tool_name"}, "", `(?i)^task$`)
	if err != nil {
		t.Fatalf("NewCondition: %v", err)
	}
	fields := &testFields{
		values: map[string]string{
			"tool_name": "Task",
		},
	}
	if regexconcern.ConditionMatch(fields, &cond) {
		t.Error("expected ConditionMatch to return false when not_pattern matches")
	}
}
