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
	checkEvent := func(system HookSystem, eventName string) {
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
