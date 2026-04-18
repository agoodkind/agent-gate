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
	ClaudeSetup              ClaudeEvent = "Setup"
	ClaudeTeammateIdle       ClaudeEvent = "TeammateIdle"
	ClaudeSessionEnd         ClaudeEvent = "SessionEnd"
)

// CanBlockClaude returns true for Claude events where exit code 2 causes the
// action to be blocked. Only pre-hooks are blockable.
func CanBlockClaude(eventName string) bool {
	switch ClaudeEvent(eventName) {
	case ClaudePreToolUse,
		ClaudePermissionRequest,
		ClaudeUserPromptSubmit:
		return true
	}
	return false
}

// ClaudeAllow returns the stdout bytes for an allow response (exit 0).
func ClaudeAllow() []byte {
	return []byte("{}\n")
}

// ClaudeBlock returns the stderr bytes for a block response.
// The caller must write these to stderr and exit with code 2.
// Claude receives the stderr text as feedback explaining why the action was blocked.
func ClaudeBlock(ruleName, message string) []byte {
	return []byte(fmt.Sprintf("agent-gate: [%s] %s\n", ruleName, message))
}
