package hook

import "fmt"

// ClaudeEvent enumerates every known Claude Code hook event name (PascalCase).
// Source: Claude Code hooks documentation, April 2026.
type ClaudeEvent string

const (
	ClaudeSessionStart       ClaudeEvent = "SessionStart"
	ClaudeUserPromptSubmit   ClaudeEvent = "UserPromptSubmit"
	ClaudePreToolUse         ClaudeEvent = "PreToolUse"
	ClaudePermissionRequest  ClaudeEvent = "PermissionRequest"
	ClaudePermissionDenied   ClaudeEvent = "PermissionDenied"
	ClaudePostToolUse        ClaudeEvent = "PostToolUse"
	ClaudePostToolUseFailure ClaudeEvent = "PostToolUseFailure"
	ClaudeNotification       ClaudeEvent = "Notification"
	ClaudeSubagentStart      ClaudeEvent = "SubagentStart"
	ClaudeSubagentStop       ClaudeEvent = "SubagentStop"
	ClaudeTaskCreated        ClaudeEvent = "TaskCreated"
	ClaudeTaskCompleted      ClaudeEvent = "TaskCompleted"
	ClaudeStop               ClaudeEvent = "Stop"
	ClaudeStopFailure        ClaudeEvent = "StopFailure"
	ClaudePreCompact         ClaudeEvent = "PreCompact"
	ClaudePostCompact        ClaudeEvent = "PostCompact"
	ClaudeInstructionsLoaded ClaudeEvent = "InstructionsLoaded"
	ClaudeConfigChange       ClaudeEvent = "ConfigChange"
	ClaudeCwdChanged         ClaudeEvent = "CwdChanged"
	ClaudeFileChanged        ClaudeEvent = "FileChanged"
	ClaudeWorktreeCreate     ClaudeEvent = "WorktreeCreate"
	ClaudeWorktreeRemove     ClaudeEvent = "WorktreeRemove"
	ClaudeElicitation        ClaudeEvent = "Elicitation"
	ClaudeElicitationResult  ClaudeEvent = "ElicitationResult"
	ClaudeTeammateIdle       ClaudeEvent = "TeammateIdle"
	ClaudeSessionEnd         ClaudeEvent = "SessionEnd"
)

// ClaudePayload holds Claude-specific fields extracted from a RawPayload.
type ClaudePayload struct {
	Event     ClaudeEvent
	SessionID string
	CWD       string
	// ToolName is set for PreToolUse, PostToolUse, PermissionRequest, etc.
	ToolName string
	// ToolInput is the tool arguments map (e.g. {"command": "..."} for Bash).
	ToolInput map[string]any
	// Prompt is set for UserPromptSubmit.
	Prompt string
	// Source distinguishes subtypes within events (e.g. SessionStart source: startup/resume).
	Source string
	// FilePath is set for FileChanged, WorktreeCreate/Remove, etc.
	FilePath string
}

// ParseClaude extracts a typed ClaudePayload from a RawPayload.
func ParseClaude(p RawPayload) ClaudePayload {
	cp := ClaudePayload{
		Event:     ClaudeEvent(p.EventName()),
		SessionID: p.SessionID(),
		CWD:       p.CWD(),
	}
	cp.ToolName, _ = p["tool_name"].(string)
	cp.ToolInput, _ = p["tool_input"].(map[string]any)
	cp.Prompt, _ = p["prompt"].(string)
	cp.Source, _ = p["source"].(string)
	cp.FilePath, _ = p["file_path"].(string)
	return cp
}

// ClaudeAllow returns the stdout bytes for an allow response (exit 0).
func ClaudeAllow() []byte {
	return []byte("{}\n")
}

// ClaudeBlock returns the stderr bytes for a block response.
// The caller must write these to stderr and exit with code 2.
// Claude receives the stderr text as feedback explaining why the action was blocked.
func ClaudeBlock(ruleName, message string) []byte {
	return []byte(fmt.Sprintf("hookguard: [%s] %s\n", ruleName, message))
}
