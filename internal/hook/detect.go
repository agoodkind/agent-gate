// Package hook decodes hook payloads emitted by supported agent hosts
// (Claude, Cursor, Codex, Gemini, VS Code, Copilot) into a closed set of
// typed events that the rules engine can evaluate.
package hook

import (
	"encoding/json"
	"unicode"
	"unicode/utf8"
)

// DetectionPayload is the shallow envelope used to identify which agent
// host produced a hook payload before the full schema is resolved.
type DetectionPayload struct {
	HookEventName         string          `json:"hook_event_name,omitempty"`
	CursorVersion         string          `json:"cursor_version,omitempty"`
	ConversationID        string          `json:"conversation_id,omitempty"`
	GenerationID          string          `json:"generation_id,omitempty"`
	WorkspaceRoots        []string        `json:"workspace_roots,omitempty"`
	UserEmail             string          `json:"user_email,omitempty"`
	TranscriptPath        string          `json:"transcript_path,omitempty"`
	PermissionMode        string          `json:"permission_mode,omitempty"`
	AgentID               string          `json:"agent_id,omitempty"`
	AgentType             string          `json:"agent_type,omitempty"`
	CopilotSessionID      string          `json:"sessionId,omitempty"`
	CopilotTranscriptPath string          `json:"transcriptPath,omitempty"`
	CopilotToolName       string          `json:"toolName,omitempty"`
	CopilotToolUseID      string          `json:"toolUseId,omitempty"`
	CopilotToolInput      json.RawMessage `json:"toolInput,omitempty"`
}

func hasCursorPayload(p DetectionPayload) bool {
	return p.CursorVersion != "" ||
		p.ConversationID != "" ||
		p.GenerationID != "" ||
		len(p.WorkspaceRoots) > 0 ||
		p.UserEmail != ""
}

func hasCopilotPayload(p DetectionPayload) bool {
	return p.CopilotSessionID != "" ||
		p.CopilotTranscriptPath != "" ||
		p.CopilotToolName != "" ||
		p.CopilotToolUseID != "" ||
		(len(p.CopilotToolInput) > 0 && string(p.CopilotToolInput) != "null")
}

func hasCursorEvent(p DetectionPayload) bool {
	name := p.HookEventName
	if name == "" {
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return unicode.IsLower(r)
}

func hasGeminiEvent(p DetectionPayload) bool {
	switch GeminiEvent(p.HookEventName) {
	case GeminiBeforeTool,
		GeminiAfterTool,
		GeminiBeforeAgent,
		GeminiAfterAgent,
		GeminiBeforeModel,
		GeminiAfterModel,
		GeminiBeforeToolSelection,
		GeminiPreCompress:
		return true
	case GeminiSessionStart,
		GeminiSessionEnd,
		GeminiNotification:
		return false
	}
	return false
}

func hasClaudePayload(p DetectionPayload) bool {
	return p.TranscriptPath != "" ||
		p.PermissionMode != "" ||
		p.AgentID != "" ||
		p.AgentType != ""
}
