package hook

// HookSystem identifies which tool invoked agent-gate.
//
// SystemUnknown is a real classification, not a sentinel for "not yet
// decided". It means detection ran every priority layer and none matched, so
// detection gaps are visible in the audit log rather than silently
// misattributed.
type HookSystem int

type hookSystemName string

const (
	hookSystemNameClaude  hookSystemName = "claude"
	hookSystemNameCodex   hookSystemName = "codex"
	hookSystemNameCopilot hookSystemName = "copilot"
	hookSystemNameCursor  hookSystemName = "cursor"
	hookSystemNameGemini  hookSystemName = "gemini"
	hookSystemNameVSCode  hookSystemName = "vscode"
)

// HookSystem variants. Each constant tags a single detected agent host.
const (
	// SystemUnknown means detection ran but no agent host matched.
	SystemUnknown HookSystem = iota
	// SystemClaude identifies the Anthropic Claude CLI/desktop host.
	SystemClaude
	// SystemCursor identifies the Cursor IDE host.
	SystemCursor
	// SystemCodex identifies the OpenAI Codex CLI host.
	SystemCodex
	// SystemGemini identifies the Google Gemini CLI host.
	SystemGemini
	// SystemVSCode identifies the VS Code editor host.
	SystemVSCode
	// SystemCopilot identifies the GitHub Copilot host.
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

// SystemFromString parses the lowercase label produced by [HookSystem.String].
// Unknown labels yield [SystemUnknown].
func SystemFromString(s string) HookSystem {
	switch hookSystemName(s) {
	case hookSystemNameClaude:
		return SystemClaude
	case hookSystemNameCursor:
		return SystemCursor
	case hookSystemNameCodex:
		return SystemCodex
	case hookSystemNameGemini:
		return SystemGemini
	case hookSystemNameVSCode:
		return SystemVSCode
	case hookSystemNameCopilot:
		return SystemCopilot
	default:
		return SystemUnknown
	}
}

// Decision is the outcome of processing a hook event.
type Decision int

// Decision variants.
const (
	// DecisionAllow lets the hook event proceed.
	DecisionAllow Decision = iota
	// DecisionBlock denies the hook event.
	DecisionBlock
)

// String returns "allow" or "block".
func (d Decision) String() string {
	if d == DecisionBlock {
		return "block"
	}
	return "allow"
}
