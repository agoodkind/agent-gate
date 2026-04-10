package hook

import "unicode"

// Detect determines which hook system called hookguard by examining hook_event_name.
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
	runes := []rune(name)
	if unicode.IsUpper(runes[0]) {
		return SystemClaude
	}
	return SystemCursor
}
