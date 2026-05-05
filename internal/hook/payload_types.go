package hook

import "goodkind.io/agent-gate/internal/rules"

// HookPayload is the demultiplexed pair of agent host plus event payload.
type HookPayload struct {
	System HookSystem
	Event  HookEvent
}

// HookEvent is the closed interface implemented by every concrete hook
// payload. Implementations expose the canonical event name, session id,
// working directory, and a flattened [rules.FieldSet] for rule evaluation.
type HookEvent interface {
	EventName() string
	SessionID() string
	CWD() string
	Fields() rules.FieldSet
}

// UnknownPayload is the fallback shape used when an event cannot be matched
// against any known agent host schema.
type UnknownPayload struct {
	HookEventName string `json:"hook_event_name"`
	Session       string `json:"session_id"`
	Conversation  string `json:"conversation_id"`
	WorkingDir    string `json:"cwd"`
}

// EventName returns the literal event name carried in the payload.
func (p UnknownPayload) EventName() string { return p.HookEventName }

// SessionID returns the session identifier, falling back to the
// conversation identifier when the session field is empty.
func (p UnknownPayload) SessionID() string {
	return firstNonEmpty(p.Session, p.Conversation)
}

// CWD returns the working directory recorded in the payload.
func (p UnknownPayload) CWD() string { return p.WorkingDir }

// Fields renders the unknown payload as a sparse [rules.FieldSet].
func (p UnknownPayload) Fields() rules.FieldSet {
	var fields rules.FieldSet
	fields.HookEventName = p.HookEventName
	fields.SessionID = p.Session
	fields.ConversationID = p.Conversation
	fields.CWD = p.WorkingDir
	return fields
}

// EventName forwards to the embedded [HookEvent], or returns the empty
// string when no event is set.
func (p HookPayload) EventName() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.EventName()
}

// SessionID forwards to the embedded [HookEvent], or returns the empty
// string when no event is set.
func (p HookPayload) SessionID() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.SessionID()
}

// CWD forwards to the embedded [HookEvent], or returns the empty string
// when no event is set.
func (p HookPayload) CWD() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.CWD()
}

// Fields forwards to the embedded [HookEvent], or returns a zero-value
// [rules.FieldSet] when no event is set.
func (p HookPayload) Fields() rules.FieldSet {
	if p.Event == nil {
		var empty rules.FieldSet
		return empty
	}
	return p.Event.Fields()
}
