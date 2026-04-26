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

const (
	// Session lifecycle.
	CursorSessionStart CursorEvent = "sessionStart"
	CursorSessionEnd   CursorEvent = "sessionEnd"

	// Generic tool use hooks (fire for all tools, including shell and MCP).
	CursorPreToolUse         CursorEvent = "preToolUse"
	CursorPostToolUse        CursorEvent = "postToolUse"
	CursorPostToolUseFailure CursorEvent = "postToolUseFailure"

	// Shell execution.
	CursorBeforeShellExecution CursorEvent = "beforeShellExecution"
	CursorAfterShellExecution  CursorEvent = "afterShellExecution"

	// MCP tool execution.
	CursorBeforeMCPExecution CursorEvent = "beforeMCPExecution"
	CursorAfterMCPExecution  CursorEvent = "afterMCPExecution"

	// File access and edits.
	CursorBeforeReadFile CursorEvent = "beforeReadFile"
	CursorAfterFileEdit  CursorEvent = "afterFileEdit"

	// Prompt submission.
	CursorBeforeSubmitPrompt CursorEvent = "beforeSubmitPrompt"

	// Subagent (Task tool) lifecycle.
	CursorSubagentStart CursorEvent = "subagentStart"
	CursorSubagentStop  CursorEvent = "subagentStop"

	// Context window compaction.
	CursorPreCompact CursorEvent = "preCompact"

	// Agent completion.
	CursorStop CursorEvent = "stop"

	// Agent output observation (informational only).
	CursorAfterAgentResponse CursorEvent = "afterAgentResponse"
	CursorAfterAgentThought  CursorEvent = "afterAgentThought"

	// Tab (inline completion) hooks.
	CursorBeforeTabFileRead CursorEvent = "beforeTabFileRead"
	CursorAfterTabFileEdit  CursorEvent = "afterTabFileEdit"
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
	b, _ := json.Marshal(cursorResponse{Permission: "allow"})
	return append(b, '\n')
}

// CursorBlock returns stdout JSON bytes for a deny response (exit 0).
// Sets both user_message (shown to the user) and agent_message (fed back to
// the agent), so the blocking rule name and violation_message reach the model
// instead of Cursor's generic canned rejection.
func CursorBlock(ruleName, message string) []byte {
	text := fmt.Sprintf("agent-gate: [%s] %s", ruleName, message)
	return CursorBlockText(text)
}

func CursorBlockText(text string) []byte {
	b, _ := json.Marshal(cursorResponse{
		Permission:   "deny",
		UserMessage:  text,
		AgentMessage: text,
	})
	return append(b, '\n')
}
