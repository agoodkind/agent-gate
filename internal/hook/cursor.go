package hook

import (
	"encoding/json"
	"fmt"
)

// CursorEvent enumerates every known Cursor hook event name (camelCase).
// Source: Cursor hooks documentation, April 2026.
// Events are grouped by category: session lifecycle, generic tool hooks,
// shell, MCP, file, prompt, subagent, compaction, agent output, and Tab.
type CursorEvent string

// CursorEvent variants. Each constant is a literal Cursor hook event name.
const (
	// CursorSessionStart fires when a Cursor session starts.
	CursorSessionStart CursorEvent = "sessionStart"
	// CursorSessionEnd fires when a Cursor session ends.
	CursorSessionEnd CursorEvent = "sessionEnd"
	// CursorPreToolUse fires before any tool invocation.
	CursorPreToolUse CursorEvent = "preToolUse"
	// CursorPostToolUse fires after a successful tool invocation.
	CursorPostToolUse CursorEvent = "postToolUse"
	// CursorPostToolUseFailure fires after a failed tool invocation.
	CursorPostToolUseFailure CursorEvent = "postToolUseFailure"
	// CursorBeforeShellExecution fires before a shell command runs.
	CursorBeforeShellExecution CursorEvent = "beforeShellExecution"
	// CursorAfterShellExecution fires after a shell command runs.
	CursorAfterShellExecution CursorEvent = "afterShellExecution"
	// CursorBeforeMCPExecution fires before an MCP tool is invoked.
	CursorBeforeMCPExecution CursorEvent = "beforeMCPExecution"
	// CursorAfterMCPExecution fires after an MCP tool returns.
	CursorAfterMCPExecution CursorEvent = "afterMCPExecution"
	// CursorBeforeReadFile fires before a file is read.
	CursorBeforeReadFile CursorEvent = "beforeReadFile"
	// CursorAfterFileEdit fires after a file edit is applied.
	CursorAfterFileEdit CursorEvent = "afterFileEdit"
	// CursorBeforeSubmitPrompt fires before the user prompt is submitted.
	CursorBeforeSubmitPrompt CursorEvent = "beforeSubmitPrompt"
	// CursorSubagentStart fires when a subagent task starts.
	CursorSubagentStart CursorEvent = "subagentStart"
	// CursorSubagentStop fires when a subagent task stops.
	CursorSubagentStop CursorEvent = "subagentStop"
	// CursorPreCompact fires before context window compaction.
	CursorPreCompact CursorEvent = "preCompact"
	// CursorStop fires when the agent stops.
	CursorStop CursorEvent = "stop"
	// CursorAfterAgentResponse fires after each agent response (observational).
	CursorAfterAgentResponse CursorEvent = "afterAgentResponse"
	// CursorAfterAgentThought fires after agent thought events (observational).
	CursorAfterAgentThought CursorEvent = "afterAgentThought"
	// CursorBeforeTabFileRead fires before Tab inline completion reads a file.
	CursorBeforeTabFileRead CursorEvent = "beforeTabFileRead"
	// CursorAfterTabFileEdit fires after Tab inline completion edits a file.
	CursorAfterTabFileEdit CursorEvent = "afterTabFileEdit"
)

// CanBlockCursor returns true for Cursor events where exit code 2 or
// permission:"deny" actually prevents the action. Only pre-hooks are blockable;
// post and observational hooks are fire-and-forget.
func CanBlockCursor(eventName string) bool {
	switch CursorEvent(eventName) {
	case CursorPreToolUse,
		CursorBeforeShellExecution,
		CursorBeforeMCPExecution,
		CursorBeforeReadFile,
		CursorSubagentStart,
		CursorBeforeSubmitPrompt,
		CursorBeforeTabFileRead:
		return true
	}
	return false
}

// cursorResponse is the JSON structure Cursor reads from stdout.
// Field names are snake_case per Cursor hooks documentation.
type cursorResponse struct {
	Permission   string `json:"permission,omitempty"`
	Continue     *bool  `json:"continue,omitempty"`
	UserMessage  string `json:"user_message,omitempty"`
	AgentMessage string `json:"agent_message,omitempty"`
}

// CursorAllow returns stdout JSON bytes for an allow response (exit 0).
func CursorAllow() []byte {
	resp := cursorResponse{
		Permission:   "allow",
		Continue:     nil,
		UserMessage:  "",
		AgentMessage: "",
	}
	b, err := json.Marshal(resp)
	if err != nil {
		// cursorResponse only carries strings and a *bool, so json.Marshal
		// cannot fail in practice. Fall back to a literal allow response.
		return []byte(`{"permission":"allow"}` + "\n")
	}
	return append(b, '\n')
}

// CursorBlock returns stdout JSON bytes for a deny response (exit 0).
// Sets both user_message (shown to the user) and agent_message (fed back to
// the agent), so the blocking rule name and violation_message reach the
// model instead of Cursor's generic canned rejection.
func CursorBlock(ruleName, message string) []byte {
	text := fmt.Sprintf("agent-gate: [%s] %s", ruleName, message)
	return CursorBlockText(text)
}

// CursorBlockText returns stdout JSON bytes for a deny response (exit 0)
// carrying the given free-form text in both message channels.
func CursorBlockText(text string) []byte {
	resp := cursorResponse{
		Permission:   "deny",
		Continue:     nil,
		UserMessage:  text,
		AgentMessage: text,
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return []byte(`{"permission":"deny"}` + "\n")
	}
	return append(b, '\n')
}

func renderCursorResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return Response{
			Stdout:   CursorBlockText(request.DiagnosticText),
			Stderr:   nil,
			ExitCode: 0,
		}
	}
	return Response{Stdout: CursorAllow(), Stderr: nil, ExitCode: 0}
}
