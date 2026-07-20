package hook

// ResponseCapability describes model-facing response fields that a provider
// accepts for one hook event. It is intentionally independent from the
// enforcement capability table because a post-event can be unable to block
// while still being able to inject context or replace a tool result.
type ResponseCapability uint8

const (
	// ResponseCapabilityNone means the provider event accepts no model-facing
	// injection or mutation response.
	ResponseCapabilityNone ResponseCapability = 0
	// ResponseCapabilityInject adds model-facing context.
	ResponseCapabilityInject ResponseCapability = 1 << iota
	// ResponseCapabilityPromptMutation replaces a prompt string.
	ResponseCapabilityPromptMutation
	// ResponseCapabilityToolInputMutation replaces structured tool input.
	ResponseCapabilityToolInputMutation
	// ResponseCapabilityToolOutputMutation replaces structured tool output.
	ResponseCapabilityToolOutputMutation
)

// Supports reports whether capability includes wanted.
func (c ResponseCapability) Supports(wanted ResponseCapability) bool {
	return c&wanted == wanted
}

// responseCapabilityTable is the documented native response surface. Entries
// absent from this table are intentionally no-ops so a newly observed event
// cannot inject or mutate context before its contract is verified.
var responseCapabilityTable = map[capabilityKey]ResponseCapability{
	{SystemClaude, "SessionStart"}:        ResponseCapabilityInject,
	{SystemClaude, "Setup"}:               ResponseCapabilityInject,
	{SystemClaude, "SubagentStart"}:       ResponseCapabilityInject,
	{SystemClaude, "UserPromptSubmit"}:    ResponseCapabilityInject,
	{SystemClaude, "UserPromptExpansion"}: ResponseCapabilityInject,
	{SystemClaude, "PreToolUse"}:          ResponseCapabilityInject | ResponseCapabilityToolInputMutation,
	{SystemClaude, "PostToolUse"}:         ResponseCapabilityInject | ResponseCapabilityToolOutputMutation,
	{SystemClaude, "PostToolUseFailure"}:  ResponseCapabilityInject,
	{SystemClaude, "PostToolBatch"}:       ResponseCapabilityInject,
	{SystemClaude, "Stop"}:                ResponseCapabilityInject,
	{SystemClaude, "SubagentStop"}:        ResponseCapabilityInject,

	{SystemCodex, "SessionStart"}:     ResponseCapabilityInject,
	{SystemCodex, "SubagentStart"}:    ResponseCapabilityInject,
	{SystemCodex, "UserPromptSubmit"}: ResponseCapabilityInject,
	{SystemCodex, "PreToolUse"}:       ResponseCapabilityInject | ResponseCapabilityToolInputMutation,
	{SystemCodex, "PostToolUse"}:      ResponseCapabilityInject,

	{SystemCursor, "sessionStart"}: ResponseCapabilityInject,
	{SystemCursor, "stop"}:         ResponseCapabilityInject,
	{SystemCursor, "postToolUse"}:  ResponseCapabilityInject | ResponseCapabilityToolOutputMutation,

	{SystemCopilot, "sessionStart"}:          ResponseCapabilityInject,
	{SystemCopilot, "SessionStart"}:          ResponseCapabilityInject,
	{SystemCopilot, "subagentStart"}:         ResponseCapabilityInject,
	{SystemCopilot, "SubagentStart"}:         ResponseCapabilityInject,
	{SystemCopilot, "postToolUse"}:           ResponseCapabilityInject | ResponseCapabilityToolOutputMutation,
	{SystemCopilot, "PostToolUse"}:           ResponseCapabilityInject | ResponseCapabilityToolOutputMutation,
	{SystemCopilot, "postToolUseFailure"}:    ResponseCapabilityInject,
	{SystemCopilot, "PostToolUseFailure"}:    ResponseCapabilityInject,
	{SystemCopilot, "notification"}:          ResponseCapabilityInject,
	{SystemCopilot, "Notification"}:          ResponseCapabilityInject,
	{SystemCopilot, "userPromptTransformed"}: ResponseCapabilityInject | ResponseCapabilityPromptMutation,
	{SystemCopilot, "preToolUse"}:            ResponseCapabilityToolInputMutation,
	{SystemCopilot, "PreToolUse"}:            ResponseCapabilityToolInputMutation,
}

// LookupResponseCapability returns the native response capabilities for a
// provider and event. Unknown pairs deliberately return none.
func LookupResponseCapability(system System, event string) ResponseCapability {
	return responseCapabilityTable[capabilityKey{system, event}]
}

func responseMutationTarget(capability ResponseCapability) (string, bool) {
	if capability.Supports(ResponseCapabilityPromptMutation) {
		return "prompt", false
	}
	if capability.Supports(ResponseCapabilityToolInputMutation) {
		return "tool_input", true
	}
	if capability.Supports(ResponseCapabilityToolOutputMutation) {
		return "tool_output", true
	}
	return "", false
}
