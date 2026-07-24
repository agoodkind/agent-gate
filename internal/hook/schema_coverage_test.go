package hook

import (
	"os"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func TestSchemaPathsHaveTypedSelectors(t *testing.T) {
	for _, path := range virtualFields {
		if config.CompileFieldSelector(path) == config.FieldSelectorInvalid {
			t.Fatalf("virtual field %q has no typed selector", path)
		}
	}

	checkSchema := func(system string, eventName string, schema EventSchema) {
		t.Helper()
		for path := range schema {
			if !ruleVisiblePath(path) {
				continue
			}
			if config.CompileFieldSelector(path) == config.FieldSelectorInvalid {
				t.Fatalf("%s %s field %q has no typed selector", system, eventName, path)
			}
		}
	}

	for eventName, schema := range cursorSchema {
		checkSchema("cursor", string(eventName), schema)
	}
	for eventName, schema := range claudeSchema {
		checkSchema("claude", string(eventName), schema)
	}
	for eventName, schema := range codexSchema {
		checkSchema("codex", string(eventName), schema)
	}
	for eventName, schema := range copilotSchema {
		checkSchema("copilot", eventName, schema)
	}
	for eventName, schema := range geminiSchema {
		checkSchema("gemini", string(eventName), schema)
	}
}

func ruleVisiblePath(path string) bool {
	switch path {
	case "permission_suggestions", "globs":
		return false
	default:
		return true
	}
}

func TestKnownEventsDecodeToConcretePayloads(t *testing.T) {
	checkEvent := func(system System, eventName string) {
		t.Helper()
		rawPayload := []byte(`{"hook_event_name":"` + eventName + `"}`)
		payload, err := ParseHookPayload(system, rawPayload)
		if err != nil {
			t.Fatalf("ParseHookPayload(%s, %s): %v", system.String(), eventName, err)
		}
		if _, ok := payload.Event.(UnknownPayload); ok {
			t.Fatalf("ParseHookPayload(%s, %s) returned UnknownPayload", system.String(), eventName)
		}
		if payload.EventName() != eventName {
			t.Fatalf("EventName() = %q, want %q", payload.EventName(), eventName)
		}
	}

	for eventName := range cursorSchema {
		checkEvent(SystemCursor, string(eventName))
	}
	for eventName := range claudeSchema {
		checkEvent(SystemClaude, string(eventName))
	}
	for eventName := range codexSchema {
		checkEvent(SystemCodex, string(eventName))
	}
	for eventName := range copilotSchema {
		checkEvent(SystemCopilot, eventName)
	}
	for eventName := range geminiSchema {
		checkEvent(SystemGemini, string(eventName))
	}
}

func TestPayloadAndAuditAPIsStayStructured(t *testing.T) {
	productionFiles := []string{
		"../audit/logger.go",
		"../hook/payload_types.go",
		"../hook/payload_decode.go",
		"../hook/provider.go",
		"../rules/engine.go",
		"../rules/fields.go",
	}
	blockedTerms := []string{
		"map[string]" + "any",
		"interface" + "{}",
		" any)",
		" any,",
		" any `",
	}
	for _, filePath := range productionFiles {
		t.Run(filePath, func(t *testing.T) {
			contentsBytes, err := os.ReadFile(filePath)
			if err != nil {
				t.Fatalf("read %s: %v", filePath, err)
			}
			contents := string(contentsBytes)
			for _, blockedTerm := range blockedTerms {
				if strings.Contains(contents, blockedTerm) {
					t.Fatalf("%s contains unstructured API term %q", filePath, blockedTerm)
				}
			}
		})
	}
}

func TestTemporalSelectorsAreScopedToExecResponseConditions(t *testing.T) {
	tests := []struct {
		name      string
		rule      config.Rule
		wantError bool
	}{
		{
			name: "last user message in exec",
			rule: config.Rule{
				Name: "prompt-validator", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"last_user_message"},
				}},
			},
		},
		{
			name: "last response output in exec",
			rule: config.Rule{
				Name: "response-validator", Events: []string{"Stop"}, Action: config.ActionInject,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"last_response_output"},
				}},
			},
		},
		{
			name: "response output in inject exec",
			rule: config.Rule{
				Name: "fallback-validator", Events: []string{"Stop"}, Action: config.ActionInject,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"response_output"},
				}},
			},
		},
		{
			name: "last user message at rule level",
			rule: config.Rule{
				Name: "invalid-rule-level", Events: []string{"Stop"}, Action: config.ActionBlock,
				FieldPaths: []string{"last_user_message"},
			},
			wantError: true,
		},
		{
			name: "last response output in regex",
			rule: config.Rule{
				Name: "invalid-regex", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindRegex), FieldPaths: []string{"last_response_output"},
				}},
			},
			wantError: true,
		},
		{
			name: "response output in blocking exec",
			rule: config.Rule{
				Name: "invalid-block", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"response_output"},
				}},
			},
			wantError: true,
		},
		{
			name: "last response output in blocking exec",
			rule: config.Rule{
				Name: "invalid-last-response-block", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"last_response_output"},
				}},
			},
			wantError: true,
		},
		{
			name: "last response output in audit exec",
			rule: config.Rule{
				Name: "invalid-last-response-audit", Events: []string{"Stop"}, Action: config.ActionAudit,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindExec), FieldPaths: []string{"last_response_output"},
				}},
			},
			wantError: true,
		},
		{
			name: "last user message in infer input",
			rule: config.Rule{
				Name: "invalid-infer-input", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindInfer), InputField: "last_user_message",
				}},
			},
			wantError: true,
		},
		{
			name: "last response output in infer cache key",
			rule: config.Rule{
				Name: "invalid-infer-cache", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind: string(config.ConditionKindInfer), CacheKey: "last_response_output",
				}},
			},
			wantError: true,
		},
		{
			name: "response output in infer context selector",
			rule: config.Rule{
				Name: "invalid-infer-context", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind:                  string(config.ConditionKindInfer),
					ContextWorkspaceField: "response_output",
				}},
			},
			wantError: true,
		},
		{
			name: "last user message in judge context",
			rule: config.Rule{
				Name:         "invalid-judge-context",
				Events:       []string{"Stop"},
				Action:       config.ActionBlock,
				JudgeContext: []string{"last_user_message"},
			},
			wantError: true,
		},
		{
			name: "whitespace padded last user message in judge context",
			rule: config.Rule{
				Name:         "invalid-padded-judge-context",
				Events:       []string{"Stop"},
				Action:       config.ActionBlock,
				JudgeContext: []string{" last_user_message "},
			},
			wantError: true,
		},
		{
			name: "last response output in diff field pair",
			rule: config.Rule{
				Name: "invalid-diff", Events: []string{"Stop"}, Action: config.ActionBlock,
				Conditions: []config.Condition{{
					Kind:      string(config.ConditionKindDiff),
					FieldPair: "last_response_output,tool_input.new_string",
				}},
			},
			wantError: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := &config.Config{Rules: []config.Rule{testCase.rule}}
			errs := ValidateConfig(cfg)
			if (len(errs) > 0) != testCase.wantError {
				t.Fatalf("ValidateConfig() errors = %v, wantError = %v", errs, testCase.wantError)
			}
		})
	}
}
