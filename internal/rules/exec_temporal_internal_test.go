package rules

import (
	"testing"
	"time"
	"unsafe"

	"goodkind.io/agent-gate/internal/hotkv"
)

func TestTemporalRecordMarshalRejectsOverflowingValue(t *testing.T) {
	var valueByte byte
	maxInt := int(^uint(0) >> 1)
	overflowingLength := maxInt - temporalRecordHeaderBytes + 1
	overflowingValue := unsafe.String(&valueByte, overflowingLength)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("MarshalBinary panicked: %v", recovered)
		}
	}()

	_, err := (temporalRecord{
		receiptID: 1,
		available: true,
		value:     overflowingValue,
	}).MarshalBinary()
	if err == nil {
		t.Fatal("MarshalBinary error = nil, want oversized record error")
	}
}

func TestTemporalRecordMarshalRoundTripsValue(t *testing.T) {
	record, err := (temporalRecord{
		receiptID: 42,
		available: true,
		value:     "response",
	}).MarshalBinary()
	if err != nil {
		t.Fatalf("MarshalBinary: %v", err)
	}

	receiptID, available, value, valid := decodeTemporalRecord(record)
	if !valid || receiptID != 42 || !available || value != "response" {
		t.Fatalf(
			"decodeTemporalRecord = (%d, %v, %q, %v), want (42, true, response, true)",
			receiptID,
			available,
			value,
			valid,
		)
	}
}

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

func TestTemporalReceiptOrderingAcrossOverlappingReloadRuntimes(t *testing.T) {
	store := hotkv.New(hotkv.Options{PruneInterval: 0})
	defer store.Close()
	olderRuntime := NewExecRuntimeWithCache(nil, nil, store)
	newerRuntime := NewExecRuntimeWithCache(nil, nil, store)
	fields := FieldSet{ConversationID: "conversation-1"}
	olderRead := make(chan struct{})
	newerStored := make(chan struct{})
	olderRuntime.temporal.afterRead = func() {
		close(olderRead)
		select {
		case <-newerStored:
		case <-time.After(250 * time.Millisecond):
		}
	}

	olderDone := make(chan struct{})
	go func() {
		olderRuntime.ObserveUserPrompt("cursor", fields, 10, "older prompt")
		close(olderDone)
	}()
	<-olderRead

	go func() {
		newerRuntime.ObserveUserPrompt("cursor", fields, 20, "newer prompt")
		close(newerStored)
	}()
	<-olderDone
	<-newerStored

	value, available := newerRuntime.lastUserMessage("cursor", fields)
	if !available || value != "newer prompt" {
		t.Fatalf(
			"last user message = (%q, %v), want newer receipt value",
			value,
			available,
		)
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
