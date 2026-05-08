package regex_test

import (
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

func TestEvalFieldMatches_Match(t *testing.T) {
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

	matches := regexconcern.EvalFieldMatches(fields, selectors, re, 0)

	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d: %#v", len(matches), matches)
	}

	m := matches[0]
	if m.FieldPath != "tool_input.content" {
		t.Errorf("FieldPath = %q, want %q", m.FieldPath, "tool_input.content")
	}
	matchText := m.Value[m.Start:m.End]
	if matchText != "AKIAIOSFODNN7EXAMPLE" {
		t.Errorf("matched text = %q, want %q", matchText, "AKIAIOSFODNN7EXAMPLE")
	}
}

func TestEvalFieldMatches_NoMatch(t *testing.T) {
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

	matches := regexconcern.EvalFieldMatches(fields, selectors, re, 0)

	if len(matches) != 0 {
		t.Errorf("expected 0 matches, got %d", len(matches))
	}
}

func TestEvalFieldMatches_EmptyField(t *testing.T) {
	pattern := `AKIA[0-9A-Z]{16}`
	re, err := intregex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile pattern: %v", err)
	}

	selectors := config.CompileFieldSelectorSpecs([]string{"tool_input.content"})
	fields := &testFields{
		values: map[string]string{},
	}

	matches := regexconcern.EvalFieldMatches(fields, selectors, re, 0)

	if len(matches) != 0 {
		t.Errorf("expected 0 matches for empty field, got %d", len(matches))
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
