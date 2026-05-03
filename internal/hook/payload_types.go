package hook

import "goodkind.io/agent-gate/internal/rules"

type HookPayload struct {
	System HookSystem
	Event  HookEvent
}

type HookEvent interface {
	EventName() string
	SessionID() string
	CWD() string
	Fields() rules.FieldSet
}

type UnknownPayload struct {
	HookEventName string `json:"hook_event_name"`
	Session       string `json:"session_id"`
	Conversation  string `json:"conversation_id"`
	WorkingDir    string `json:"cwd"`
}

func (p UnknownPayload) EventName() string { return p.HookEventName }

func (p UnknownPayload) SessionID() string {
	return firstNonEmpty(p.Session, p.Conversation)
}

func (p UnknownPayload) CWD() string { return p.WorkingDir }

func (p UnknownPayload) Fields() rules.FieldSet {
	return rules.FieldSet{
		HookEventName:  p.HookEventName,
		SessionID:      p.Session,
		ConversationID: p.Conversation,
		CWD:            p.WorkingDir,
	}
}

func (p HookPayload) EventName() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.EventName()
}

func (p HookPayload) SessionID() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.SessionID()
}

func (p HookPayload) CWD() string {
	if p.Event == nil {
		return ""
	}
	return p.Event.CWD()
}

func (p HookPayload) Fields() rules.FieldSet {
	if p.Event == nil {
		return rules.FieldSet{}
	}
	return p.Event.Fields()
}
