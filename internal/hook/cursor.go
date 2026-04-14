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
	CursorPreToolUse        CursorEvent = "preToolUse"
	CursorPostToolUse       CursorEvent = "postToolUse"
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

// isObservationalCursorEvent returns true for Cursor events that are fire-and-forget
// (informational only). These events cannot block or modify the agent's behavior,
// so violations must be deferred to the next stop hook via followup_message.
func isObservationalCursorEvent(eventName string) bool {
	switch CursorEvent(eventName) {
	case CursorAfterAgentResponse, CursorAfterAgentThought,
		CursorAfterShellExecution, CursorAfterMCPExecution,
		CursorAfterFileEdit, CursorAfterTabFileEdit,
		CursorPostToolUse, CursorPostToolUseFailure:
		return true
	}
	return false
}

// CursorPayload holds Cursor-specific fields extracted from a RawPayload.
type CursorPayload struct {
	Event          CursorEvent
	ConversationID string
	GenerationID   string
	CWD            string
	// Command is set for beforeShellExecution.
	Command string
	// FilePath is set for beforeReadFile and afterFileEdit.
	FilePath string
	// ToolName and ToolInput are set for beforeMCPExecution.
	ToolName  string
	ToolInput map[string]any
	// Prompt is set for beforeSubmitPrompt.
	Prompt string
}

// ParseCursor extracts a typed CursorPayload from a RawPayload.
func ParseCursor(p RawPayload) CursorPayload {
	cp := CursorPayload{
		Event:          CursorEvent(p.EventName()),
		ConversationID: p.SessionID(),
		CWD:            p.CWD(),
	}
	cp.GenerationID, _ = p["generation_id"].(string)
	cp.Command, _ = p["command"].(string)
	cp.FilePath, _ = p["file_path"].(string)
	cp.ToolName, _ = p["tool_name"].(string)
	cp.ToolInput, _ = p["tool_input"].(map[string]any)
	cp.Prompt, _ = p["prompt"].(string)
	return cp
}

// cursorResponse is the JSON structure Cursor reads from stdout.
type cursorResponse struct {
	Permission     string `json:"permission"`
	UserMessage    string `json:"userMessage,omitempty"`
	FollowupMessage string `json:"followup_message,omitempty"`
}

// CursorAllow returns stdout JSON bytes for an allow response (exit 0).
func CursorAllow() []byte {
	b, _ := json.Marshal(cursorResponse{Permission: "allow"})
	return append(b, '\n')
}

// CursorBlock returns stdout JSON bytes for a deny response (exit 0).
// Cursor reads the userMessage and can surface it to the user.
func CursorBlock(ruleName, message string) []byte {
	b, _ := json.Marshal(cursorResponse{
		Permission:  "deny",
		UserMessage: fmt.Sprintf("agent-gate: [%s] %s", ruleName, message),
	})
	return append(b, '\n')
}

// CursorFollowup returns stdout JSON for a stop hook response that sends
// a followup_message, causing Cursor to auto-submit it as the next user prompt.
func CursorFollowup(ruleName, message string) []byte {
	b, _ := json.Marshal(cursorResponse{
		FollowupMessage: fmt.Sprintf("agent-gate: [%s] %s", ruleName, message),
	})
	return append(b, '\n')
}
