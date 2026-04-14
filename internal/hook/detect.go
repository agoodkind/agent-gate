package hook

import (
	"unicode"
	"unicode/utf8"
)

// Detect determines which hook system called agent-gate by examining hook_event_name.
//
// Claude Code uses PascalCase event names (PreToolUse, SessionStart, Stop, ...).
// Cursor uses camelCase event names (beforeShellExecution, afterFileEdit, stop, ...).
//
// The first Unicode character of the event name is the discriminator:
// uppercase => Claude, lowercase => Cursor.
func Detect(p RawPayload) HookSystem {
	name := p.EventName()
	if name == "" {
		return SystemUnknown
	}
	r, _ := utf8.DecodeRuneInString(name)
	if unicode.IsUpper(r) {
		return SystemClaude
	}
	return SystemCursor
}
