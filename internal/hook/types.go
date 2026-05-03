package hook

// HookSystem identifies which tool invoked agent-gate.
//
// SystemUnknown is a real classification, not a sentinel for "not yet
// decided". It means detection ran every priority layer and none matched, so
// detection gaps are visible in the audit log rather than silently
// misattributed.
type HookSystem int

const (
	SystemUnknown HookSystem = iota
	SystemClaude
	SystemCursor
	SystemCodex
	SystemGemini
	SystemVSCode
	SystemCopilot
)

// String returns a lowercase label suitable for audit output.
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
	case SystemVSCode:
		return "vscode"
	case SystemCopilot:
		return "copilot"
	default:
		return "unknown"
	}
}

func SystemFromString(s string) HookSystem {
	switch s {
	case "claude":
		return SystemClaude
	case "cursor":
		return SystemCursor
	case "codex":
		return SystemCodex
	case "gemini":
		return SystemGemini
	case "vscode":
		return SystemVSCode
	case "copilot":
		return SystemCopilot
	default:
		return SystemUnknown
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
