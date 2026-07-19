package hook

// Capability describes what a (provider, event) pair can do at the hook
// boundary.
//
// The three tiers are documented in HOOKS.md under "Provider Capability
// Matrix" and were verified against the public docs for each provider on
// 2026-05-10:
//
//   - Claude:  https://code.claude.com/docs/en/hooks
//   - Codex:   https://developers.openai.com/codex/hooks
//   - Cursor:  https://cursor.com/docs/agent/hooks
//   - Gemini:  https://geminicli.com/docs/hooks/reference/
//
// Use [LookupCapability] to ask whether a (provider, event) pair can block a
// tool call, substitute its result, or only observe it.
type Capability int

// Capability tiers, ordered from weakest to strongest.
const (
	// CapabilityObserve means the hook fires after the tool ran and the
	// model has already seen the original output. A block decision is added
	// as extra context but cannot prevent the tool result from reaching the
	// model. Most after-* events are in this tier.
	CapabilityObserve Capability = iota
	// CapabilitySubstitute means the tool already ran, but exit 2 (or the
	// equivalent block decision) replaces the result the model sees with the
	// hook's stderr feedback. Codex PostToolUse is in this tier; Cursor
	// postToolUse for MCP tool results is also in this tier.
	CapabilitySubstitute
	// CapabilityBlock means the hook fires before the tool runs and a block
	// decision stops the tool from executing. All pre-* events on every
	// supported provider are in this tier.
	CapabilityBlock
)

// String returns a short label for the capability.
func (c Capability) String() string {
	switch c {
	case CapabilityBlock:
		return "block"
	case CapabilitySubstitute:
		return "substitute"
	case CapabilityObserve:
		return "observe"
	default:
		return "unknown"
	}
}

// capabilityKey is the lookup key for the capability table.
type capabilityKey struct {
	system System
	event  string
}

// capabilityTable is hand-curated from the per-provider docs. Events absent
// from this table default to CapabilityObserve (fail closed on capability
// claims).
//
// When a new event or provider is wired up, add an entry here and update the
// Provider Capability Matrix in HOOKS.md. The capability_test.go test asserts
// every event listed in the per-provider sections of HOOKS.md has an entry.
var capabilityTable = map[capabilityKey]Capability{
	// Claude: PreToolUse blocks; PostToolUse only adds context.
	{SystemClaude, "PreToolUse"}:         CapabilityBlock,
	{SystemClaude, "PostToolUse"}:        CapabilityObserve,
	{SystemClaude, "PostToolUseFailure"}: CapabilityObserve,
	{SystemClaude, "PermissionRequest"}:  CapabilityBlock,
	{SystemClaude, "PermissionDenied"}:   CapabilityObserve,
	{SystemClaude, "Stop"}:               CapabilityObserve,
	{SystemClaude, "StopFailure"}:        CapabilityObserve,
	{SystemClaude, "SubagentStop"}:       CapabilityObserve,
	{SystemClaude, "UserPromptSubmit"}:   CapabilityBlock,

	// GitHub Copilot Chat uses Claude-style event names and response
	// semantics, with VS Code-shaped tool payloads.
	{SystemCopilot, "PreToolUse"}:            CapabilityBlock,
	{SystemCopilot, "PostToolUse"}:           CapabilityObserve,
	{SystemCopilot, "Stop"}:                  CapabilityObserve,
	{SystemCopilot, "UserPromptSubmit"}:      CapabilityBlock,
	{SystemCopilot, "preToolUse"}:            CapabilityBlock,
	{SystemCopilot, "postToolUse"}:           CapabilityObserve,
	{SystemCopilot, "sessionStart"}:          CapabilityObserve,
	{SystemCopilot, "subagentStart"}:         CapabilityObserve,
	{SystemCopilot, "notification"}:          CapabilityObserve,
	{SystemCopilot, "userPromptTransformed"}: CapabilityObserve,

	// Codex: PreToolUse blocks; PostToolUse substitutes the result.
	{SystemCodex, "PreToolUse"}:        CapabilityBlock,
	{SystemCodex, "PostToolUse"}:       CapabilitySubstitute,
	{SystemCodex, "PermissionRequest"}: CapabilityBlock,
	{SystemCodex, "Stop"}:              CapabilityObserve,
	{SystemCodex, "UserPromptSubmit"}:  CapabilityBlock,

	// Cursor: pre-events block via permission field; post-events cannot
	// block (postToolUse can substitute MCP output but not non-MCP).
	{SystemCursor, "preToolUse"}:           CapabilityBlock,
	{SystemCursor, "beforeShellExecution"}: CapabilityBlock,
	{SystemCursor, "beforeMCPExecution"}:   CapabilityBlock,
	{SystemCursor, "beforeReadFile"}:       CapabilityBlock,
	{SystemCursor, "beforeSubmitPrompt"}:   CapabilityBlock,
	{SystemCursor, "beforeTabFileRead"}:    CapabilityBlock,
	{SystemCursor, "postToolUse"}:          CapabilitySubstitute,
	{SystemCursor, "postToolUseFailure"}:   CapabilityObserve,
	{SystemCursor, "afterShellExecution"}:  CapabilityObserve,
	{SystemCursor, "afterMCPExecution"}:    CapabilityObserve,
	{SystemCursor, "afterFileEdit"}:        CapabilityObserve,
	{SystemCursor, "afterAgentResponse"}:   CapabilityObserve,

	// Gemini: BeforeTool blocks via decision; AfterTool is undocumented as
	// of 2026-05-10 and treated as observe-only.
	{SystemGemini, "BeforeTool"}: CapabilityBlock,
	{SystemGemini, "AfterTool"}:  CapabilityObserve,
}

// LookupCapability returns the capability tier for a (system, event) pair.
// Pairs not in the table return CapabilityObserve. The caller is responsible
// for matching the canonical event name used in the per-provider section of
// HOOKS.md.
func LookupCapability(system System, event string) Capability {
	if c, ok := capabilityTable[capabilityKey{system, event}]; ok {
		return c
	}
	return CapabilityObserve
}
