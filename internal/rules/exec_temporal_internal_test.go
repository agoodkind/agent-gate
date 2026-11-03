package rules

import (
	"testing"

	"goodkind.io/agent-gate/internal/hotkv"
)

func TestTemporalRecordsUseInternalNamespace(t *testing.T) {
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runtime := NewExecRuntimeWithCache(nil, nil, store)
	fields := FieldSet{ConversationID: "conversation-1"}

	if !runtime.ObserveUserPrompt("cursor", fields, 1, "private prompt") {
		t.Fatal("ObserveUserPrompt did not store the prompt")
	}

	internalEntries, err := store.List(
		hotkv.InternalNamespacePrefix+"exec-temporal",
		"",
		0,
		true,
	)
	if err != nil {
		t.Fatalf("List internal temporal namespace: %v", err)
	}
	if len(internalEntries) != 1 {
		t.Fatalf("internal temporal entries = %d, want 1", len(internalEntries))
	}
	publicEntries, err := store.List("exec-temporal", "", 0, true)
	if err != nil {
		t.Fatalf("List legacy public temporal namespace: %v", err)
	}
	if len(publicEntries) != 0 {
		t.Fatalf("legacy public temporal entries = %d, want 0", len(publicEntries))
	}
}

func TestTemporalResponseLookupIsActionIndependentAndTargetScoped(t *testing.T) {
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	runtime := NewExecRuntimeWithCache(nil, nil, store)
	fields := FieldSet{ConversationID: "conversation-1"}
	if !runtime.ObserveResponseOutput(
		"copilot",
		fields,
		"userPromptTransformed",
		"inject",
		"prompt",
		1,
		"final prompt",
	) {
		t.Fatal("ObserveResponseOutput did not store the response")
	}

	tests := []struct {
		name      string
		system    string
		fields    FieldSet
		eventName string
		action    string
		target    string
		wantValue string
		wantOK    bool
	}{
		{
			name: "different action", system: "copilot", fields: fields,
			eventName: "userPromptTransformed", action: "mutate", target: "prompt",
			wantValue: "final prompt", wantOK: true,
		},
		{
			name: "different provider", system: "cursor", fields: fields,
			eventName: "userPromptTransformed", action: "mutate", target: "prompt",
		},
		{
			name: "different conversation", system: "copilot",
			fields:    FieldSet{ConversationID: "conversation-2"},
			eventName: "userPromptTransformed", action: "mutate", target: "prompt",
		},
		{
			name: "different event", system: "copilot", fields: fields,
			eventName: "postToolUse", action: "mutate", target: "prompt",
		},
		{
			name: "different target", system: "copilot", fields: fields,
			eventName: "userPromptTransformed", action: "mutate", target: "context",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			value, ok := runtime.lastResponseOutput(
				testCase.system,
				testCase.fields,
				testCase.eventName,
				testCase.action,
				testCase.target,
			)
			if value != testCase.wantValue || ok != testCase.wantOK {
				t.Fatalf(
					"lastResponseOutput() = (%q, %v), want (%q, %v)",
					value,
					ok,
					testCase.wantValue,
					testCase.wantOK,
				)
			}
		})
	}
}
