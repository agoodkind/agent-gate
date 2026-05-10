package rules_test

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules"
	concernlimit "goodkind.io/agent-gate/internal/rules/concerns/limit"
)

func TestEvaluateAllCapsConcreteMatchesPerRule(t *testing.T) {
	rule := loadRule(t,
		"no-bad-token",
		`bad-\d+`,
		[]string{"Stop"},
		[]string{"assistant_message"},
		"bad token is blocked.",
	)

	var builder strings.Builder
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString("bad-")
		builder.WriteString(strconv.Itoa(i))
	}

	got := rules.EvaluateAll(
		context.Background(),
		"codex",
		"Stop",
		testFields(map[string]any{"assistant_message": builder.String()}),
		[]config.Rule{rule},
		nil,
	)

	if len(got) != concernlimit.MaxCollectedMatchesPerEvaluation {
		t.Fatalf("EvaluateAll returned %d matches, want %d", len(got), concernlimit.MaxCollectedMatchesPerEvaluation)
	}

	lastWant := "bad-" + strconv.Itoa(concernlimit.MaxCollectedMatchesPerEvaluation-1)
	lastGot := got[len(got)-1].Value[got[len(got)-1].Start:got[len(got)-1].End]
	if lastGot != lastWant {
		t.Fatalf("last retained match = %q, want %q", lastGot, lastWant)
	}
}

func TestEvaluateAllCapsConcreteMatchesAcrossRegexConditions(t *testing.T) {
	condOne, err := config.NewCondition([]string{"assistant_message"}, `bad-\d+`, "")
	if err != nil {
		t.Fatalf("NewCondition: %v", err)
	}
	condOne.Kind = string(config.ConditionKindRegex)
	condTwo := condOne

	rule := config.Rule{
		Name:             "double-regex-cap",
		Events:           []string{"Stop"},
		Conditions:       []config.Condition{condOne, condTwo},
		Action:           "block",
		ViolationMessage: "blocked",
	}

	var builder strings.Builder
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString("bad-")
		builder.WriteString(strconv.Itoa(i))
	}

	got := rules.EvaluateAll(
		context.Background(),
		"codex",
		"Stop",
		testFields(map[string]any{"assistant_message": builder.String()}),
		[]config.Rule{rule},
		nil,
	)

	if len(got) != concernlimit.MaxCollectedMatchesPerEvaluation {
		t.Fatalf("EvaluateAll returned %d matches, want %d", len(got), concernlimit.MaxCollectedMatchesPerEvaluation)
	}

	lastWant := "bad-" + strconv.Itoa(concernlimit.MaxCollectedMatchesPerEvaluation-1)
	lastGot := got[len(got)-1].Value[got[len(got)-1].Start:got[len(got)-1].End]
	if lastGot != lastWant {
		t.Fatalf("last retained match = %q, want %q", lastGot, lastWant)
	}
}

func TestEvaluateAllCapsConcreteMatchesAcrossDiffConditions(t *testing.T) {
	const tomlBody = `
[[rules]]
name = "diff-cap"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"

[[rules.conditions]]
kind = "diff"
field_pair = "tool_input.old_string,tool_input.new_string"
pattern = '''bad-\d+'''

[[rules.conditions]]
kind = "diff"
field_pair = "last_assistant_message,assistant_message"
pattern = '''bad-\d+'''
`
	cfg := loadTOML(t, tomlBody)
	if errs := loadValidate(t, cfg); len(errs) != 0 {
		t.Fatalf("ValidateConfig: %v", errs)
	}

	var toolInputBuilder strings.Builder
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			toolInputBuilder.WriteByte(' ')
		}
		toolInputBuilder.WriteString("bad-")
		toolInputBuilder.WriteString(strconv.Itoa(i))
	}

	var assistantBuilder strings.Builder
	start := concernlimit.MaxCollectedMatchesPerEvaluation + 1000
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			assistantBuilder.WriteByte(' ')
		}
		assistantBuilder.WriteString("bad-")
		assistantBuilder.WriteString(strconv.Itoa(start + i))
	}

	fields := rules.FieldSet{
		ToolInputOldString:   "",
		ToolInputNewString:   toolInputBuilder.String(),
		LastAssistantMessage: "",
		AssistantMessage:     assistantBuilder.String(),
	}

	got := rules.EvaluateAll(context.Background(), "claude", "PreToolUse", fields, cfg.Rules, nil)
	if len(got) != concernlimit.MaxCollectedMatchesPerEvaluation {
		t.Fatalf("EvaluateAll returned %d matches, want %d", len(got), concernlimit.MaxCollectedMatchesPerEvaluation)
	}

	lastWant := "bad-" + strconv.Itoa(concernlimit.MaxCollectedMatchesPerEvaluation-1)
	lastGot := got[len(got)-1].Value[got[len(got)-1].Start:got[len(got)-1].End]
	if lastGot != lastWant {
		t.Fatalf("last retained diff match = %q, want %q", lastGot, lastWant)
	}
}
