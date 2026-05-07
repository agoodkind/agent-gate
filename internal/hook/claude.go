package hook

import "fmt"

// ClaudeEvent enumerates every known Claude Code hook event name (PascalCase).
// Source: Claude Code hooks documentation, April 2026.
type ClaudeEvent string

// ClaudeEvent variants. Each constant is a literal Claude hook event name.
const (
	// ClaudeSessionStart is emitted when a session starts.
	ClaudeSessionStart ClaudeEvent = "SessionStart"
	// ClaudeUserPromptSubmit is emitted when the user submits a prompt.
	ClaudeUserPromptSubmit ClaudeEvent = "UserPromptSubmit"
	// ClaudePreToolUse is emitted before a tool invocation.
	ClaudePreToolUse ClaudeEvent = "PreToolUse"
	// ClaudePermissionRequest is emitted on a tool permission prompt.
	ClaudePermissionRequest ClaudeEvent = "PermissionRequest"
	// ClaudePermissionDenied is emitted when a permission request is denied.
	ClaudePermissionDenied ClaudeEvent = "PermissionDenied"
	// ClaudePostToolUse is emitted after a successful tool invocation.
	ClaudePostToolUse ClaudeEvent = "PostToolUse"
	// ClaudePostToolUseFailure is emitted after a failed tool invocation.
	ClaudePostToolUseFailure ClaudeEvent = "PostToolUseFailure"
	// ClaudeNotification is emitted for ad-hoc notifications.
	ClaudeNotification ClaudeEvent = "Notification"
	// ClaudeSubagentStart is emitted when a subagent starts.
	ClaudeSubagentStart ClaudeEvent = "SubagentStart"
	// ClaudeSubagentStop is emitted when a subagent stops.
	ClaudeSubagentStop ClaudeEvent = "SubagentStop"
	// ClaudeTaskCreated is emitted when a teammate task is created.
	ClaudeTaskCreated ClaudeEvent = "TaskCreated"
	// ClaudeTaskCompleted is emitted when a teammate task completes.
	ClaudeTaskCompleted ClaudeEvent = "TaskCompleted"
	// ClaudeStop is emitted when the agent stops normally.
	ClaudeStop ClaudeEvent = "Stop"
	// ClaudeStopFailure is emitted when the agent stops with an error.
	ClaudeStopFailure ClaudeEvent = "StopFailure"
	// ClaudePreCompact is emitted before context compaction.
	ClaudePreCompact ClaudeEvent = "PreCompact"
	// ClaudePostCompact is emitted after context compaction.
	ClaudePostCompact ClaudeEvent = "PostCompact"
	// ClaudeInstructionsLoaded is emitted when instructions load.
	ClaudeInstructionsLoaded ClaudeEvent = "InstructionsLoaded"
	// ClaudeConfigChange is emitted on configuration changes.
	ClaudeConfigChange ClaudeEvent = "ConfigChange"
	// ClaudeCwdChanged is emitted when the working directory changes.
	ClaudeCwdChanged ClaudeEvent = "CwdChanged"
	// ClaudeFileChanged is emitted when a tracked file changes.
	ClaudeFileChanged ClaudeEvent = "FileChanged"
	// ClaudeWorktreeCreate is emitted when a worktree is created.
	ClaudeWorktreeCreate ClaudeEvent = "WorktreeCreate"
	// ClaudeWorktreeRemove is emitted when a worktree is removed.
	ClaudeWorktreeRemove ClaudeEvent = "WorktreeRemove"
	// ClaudeElicitation is emitted on an MCP elicitation request.
	ClaudeElicitation ClaudeEvent = "Elicitation"
	// ClaudeElicitationResult is emitted on elicitation completion.
	ClaudeElicitationResult ClaudeEvent = "ElicitationResult"
	// ClaudeSetup is emitted during environment setup.
	ClaudeSetup ClaudeEvent = "Setup"
	// ClaudeTeammateIdle is emitted when a teammate becomes idle.
	ClaudeTeammateIdle ClaudeEvent = "TeammateIdle"
	// ClaudeSessionEnd is emitted when a session ends.
	ClaudeSessionEnd ClaudeEvent = "SessionEnd"
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
	return ClaudeBlockText(fmt.Sprintf("agent-gate: [%s] %s", ruleName, message))
}

// ClaudeBlockText wraps a free-form text message into the stderr byte form
// used by [ClaudeBlock]. Callers must still exit with code 2 for the
// message to be treated as a block decision by Claude.
func ClaudeBlockText(text string) []byte {
	return []byte(text + "\n")
}

func renderClaudeResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return Response{
			Stdout:   ClaudeAllow(),
			Stderr:   ClaudeBlockText(request.DiagnosticText),
			ExitCode: 2,
		}
	}
	return Response{Stdout: ClaudeAllow(), Stderr: nil, ExitCode: 0}
}
