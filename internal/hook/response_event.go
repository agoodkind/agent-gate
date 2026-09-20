package hook

import "goodkind.io/agent-gate/internal/rules"

// ResponseEvent is the hook_event_name value for completed assistant output.
const ResponseEvent = "Response"

// ResponsePayload is the only response event shape accepted by Agent Gate.
// Provider adapters must convert native responses to these fields.
type ResponsePayload struct {
	HookEventName   string `json:"hook_event_name"`
	Session         string `json:"session_id"`
	AssistantOutput string `json:"assistant_message"`
}

// EventName supplies hook_event_name to the shared hook evaluator.
func (p ResponsePayload) EventName() string { return p.HookEventName }

// SessionID supplies the optional correlation identifier to the shared hook evaluator.
func (p ResponsePayload) SessionID() string { return p.Session }

// CWD returns an empty string. Response events have no filesystem context.
func (p ResponsePayload) CWD() string { return "" }

// Fields maps assistant_message to the deterministic rule field set.
func (p ResponsePayload) Fields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = p.HookEventName
	fields.SessionID = p.Session
	fields.AssistantMessage = p.AssistantOutput
	return fields
}
