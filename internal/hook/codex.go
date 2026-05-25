package hook

import "encoding/json"

// CodexEvent enumerates the documented Codex hook event names.
type CodexEvent string

// CodexEvent variants. Each constant is a literal Codex hook event name.
const (
	// CodexSessionStart fires when a Codex session starts.
	CodexSessionStart CodexEvent = "SessionStart"
	// CodexPreToolUse fires before a tool is invoked.
	CodexPreToolUse CodexEvent = "PreToolUse"
	// CodexPermissionRequest fires on a tool permission prompt.
	CodexPermissionRequest CodexEvent = "PermissionRequest"
	// CodexPostToolUse fires after a tool returns.
	CodexPostToolUse CodexEvent = "PostToolUse"
	// CodexUserPromptSubmit fires when the user submits a prompt.
	CodexUserPromptSubmit CodexEvent = "UserPromptSubmit"
	// CodexStop fires when a Codex turn stops.
	CodexStop CodexEvent = "Stop"
)

// CodexHookSpecificOutput is the discriminated output block carried in a
// Codex hook response.
type CodexHookSpecificOutput struct {
	HookEventName            string                   `json:"hookEventName,omitempty"`
	PermissionDecision       string                   `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string                   `json:"permissionDecisionReason,omitempty"`
	Decision                 *CodexPermissionDecision `json:"decision,omitempty"`
}

// CodexPermissionDecision is the inner permission verdict for permission
// request hooks.
type CodexPermissionDecision struct {
	Behavior string `json:"behavior,omitempty"`
	Message  string `json:"message,omitempty"`
}

type codexResponse struct {
	Continue           *bool                    `json:"continue,omitempty"`
	StopReason         string                   `json:"stopReason,omitempty"`
	SystemMessage      string                   `json:"systemMessage,omitempty"`
	SuppressOutput     *bool                    `json:"suppressOutput,omitempty"`
	Decision           string                   `json:"decision,omitempty"`
	Reason             string                   `json:"reason,omitempty"`
	HookSpecificOutput *CodexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// CodexAllow returns the stdout bytes for an allow response.
func CodexAllow() []byte {
	return []byte("{}\n")
}

// CodexBlock returns the stdout bytes for a deny response, formatting the
// agent-gate rule name plus message into the per-event reason channel.
func CodexBlock(eventName, ruleName, message string) []byte {
	text := "agent-gate: [" + ruleName + "] " + message
	return CodexBlockText(eventName, text)
}

// CodexBlockText returns the stdout bytes for a deny response carrying the
// given free-form text in whichever per-event channel Codex expects.
func CodexBlockText(eventName, text string) []byte {
	resp := codexResponse{
		Continue:           nil,
		StopReason:         "",
		SystemMessage:      "",
		SuppressOutput:     nil,
		Decision:           "",
		Reason:             "",
		HookSpecificOutput: nil,
	}

	switch CodexEvent(eventName) {
	case CodexPreToolUse:
		resp.SystemMessage = text
		resp.HookSpecificOutput = &CodexHookSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: text,
			Decision:                 nil,
		}
	case CodexPermissionRequest:
		resp.HookSpecificOutput = &CodexHookSpecificOutput{
			HookEventName:            "PermissionRequest",
			PermissionDecision:       "",
			PermissionDecisionReason: "",
			Decision: &CodexPermissionDecision{
				Behavior: "deny",
				Message:  text,
			},
		}
	case CodexPostToolUse:
		continueProcessing := false
		resp.Continue = &continueProcessing
		resp.StopReason = text
		resp.Decision = "block"
		resp.Reason = text
	case CodexUserPromptSubmit:
		resp.Decision = "block"
		resp.Reason = text
	case CodexStop, CodexSessionStart:
		return CodexAllow()
	default:
		resp.Decision = "block"
		resp.Reason = text
	}

	bytes, err := json.Marshal(resp)
	if err != nil {
		return []byte("{}\n")
	}
	return append(bytes, '\n')
}

// renderCodexResponse encodes a daemon decision for the Codex CLI hook
// protocol. At PreToolUse, exit 2 hard-blocks the tool call.
//
// At PostToolUse, exit 2 replaces the tool result the agent sees with the
// hook's stderr feedback. The tool ran, but the agent sees the daemon's
// block message instead of the original output. This is a result-substitution
// capability, not a true block. See internal/hook/capability.go and the
// Provider Capability Matrix in HOOKS.md.
func renderCodexResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return Response{
			Stdout:   CodexBlockText(request.EventName, request.DiagnosticText),
			Stderr:   nil,
			ExitCode: 0,
		}
	}
	return Response{Stdout: CodexAllow(), Stderr: nil, ExitCode: 0}
}
