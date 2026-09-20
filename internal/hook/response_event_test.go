package hook_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/hook"
)

func TestResponsePayloadExposesOnlyNeutralTextFields(t *testing.T) {
	t.Parallel()
	payload, err := hook.ParseHookPayload(
		hook.SystemResponse,
		[]byte(`{"hook_event_name":"Response","session_id":"session-1","assistant_message":"plain text"}`),
	)
	if err != nil {
		t.Fatalf("ParseHookPayload: %v", err)
	}
	if payload.EventName() != hook.ResponseEvent {
		t.Fatalf("EventName = %q, want %q", payload.EventName(), hook.ResponseEvent)
	}
	if payload.SessionID() != "session-1" {
		t.Fatalf("SessionID = %q, want session-1", payload.SessionID())
	}
	fields := payload.Fields()
	if fields.AssistantMessage != "plain text" {
		t.Fatalf("AssistantMessage = %q, want plain text", fields.AssistantMessage)
	}
	if fields.ToolName != "" || fields.ToolInputContent != "" {
		t.Fatalf("response payload exposed provider hook fields: %#v", fields)
	}
}

func TestResponseEventCanBlock(t *testing.T) {
	t.Parallel()
	if got := hook.LookupCapability(hook.SystemResponse, hook.ResponseEvent); got != hook.CapabilityBlock {
		t.Fatalf("LookupCapability = %s, want block", got)
	}
}
