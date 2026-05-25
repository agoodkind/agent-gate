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

// renderClaudeResponse encodes a daemon decision for the Claude Code hook
// protocol. Exit 2 is a strong signal at PreToolUse: the tool call is dropped
// and stderr is shown to the model as the block reason.
//
// At PostToolUse, exit 2 cannot undo the tool call. The tool already ran and
// Claude has already received its output. Hook stderr is appended as context
// for the next assistant turn, but the original tool output remains in the
// model's window. Documented at https://code.claude.com/docs/en/hooks under
// "Exit code 2 behavior per event".
//
// Rules subscribed to Claude PostToolUse trigger a config-load WARN noting
// the effective downgrade to audit. See internal/hook/capability.go and the
// Provider Capability Matrix in HOOKS.md.
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
