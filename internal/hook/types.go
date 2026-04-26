package hook

// HookSystem identifies which tool invoked agent-gate.
type HookSystem int

const (
	SystemUnknown HookSystem = iota
	SystemClaude
	SystemCursor
	SystemCodex
	SystemGemini
)

// String returns a lowercase label suitable for log output.
func (s HookSystem) String() string {
	switch s {
	case SystemClaude:
		return "claude"
	case SystemCursor:
		return "cursor"
	case SystemCodex:
		return "codex"
	case SystemGemini:
		return "gemini"
	default:
		return "unknown"
	}
}

// Decision is the outcome of processing a hook event.
type Decision int

const (
	DecisionAllow Decision = iota
	DecisionBlock
)

// String returns "allow" or "block".
func (d Decision) String() string {
	if d == DecisionBlock {
		return "block"
	}
	return "allow"
}

// RawPayload is the decoded JSON from stdin kept as a generic map.
// Both Claude and Cursor send JSON on stdin; all field access goes through this type.
type RawPayload map[string]any

// EventName extracts hook_event_name from the payload.
func (p RawPayload) EventName() string {
	v, _ := p["hook_event_name"].(string)
	return v
}

// SessionID returns session_id (Claude) or conversation_id (Cursor).
func (p RawPayload) SessionID() string {
	if v, ok := p["session_id"].(string); ok && v != "" {
		return v
	}
	v, _ := p["conversation_id"].(string)
	return v
}

// CWD returns the working directory from the payload.
func (p RawPayload) CWD() string {
	v, _ := p["cwd"].(string)
	return v
}
