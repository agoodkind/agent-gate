package hook

import "encoding/json"

// CodexEvent enumerates the documented Codex hook event names.
type CodexEvent string

const (
	CodexSessionStart      CodexEvent = "SessionStart"
	CodexPreToolUse        CodexEvent = "PreToolUse"
	CodexPermissionRequest CodexEvent = "PermissionRequest"
	CodexPostToolUse       CodexEvent = "PostToolUse"
	CodexUserPromptSubmit  CodexEvent = "UserPromptSubmit"
	CodexStop              CodexEvent = "Stop"
)

// CanBlockCodex returns true for Codex events where hook output can
// meaningfully stop or replace the normal flow.
func CanBlockCodex(eventName string) bool {
	switch CodexEvent(eventName) {
	case CodexPreToolUse,
		CodexPermissionRequest,
		CodexPostToolUse,
		CodexUserPromptSubmit,
		CodexStop:
		return true
	}
	return false
}

type CodexHookSpecificOutput struct {
	HookEventName            string                  `json:"hookEventName,omitempty"`
	PermissionDecision       string                  `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string                  `json:"permissionDecisionReason,omitempty"`
	Decision                 CodexPermissionDecision `json:"decision,omitempty"`
}

type CodexPermissionDecision struct {
	Behavior string `json:"behavior,omitempty"`
	Message  string `json:"message,omitempty"`
}

type codexResponse struct {
	Continue           *bool                   `json:"continue,omitempty"`
	StopReason         string                  `json:"stopReason,omitempty"`
	SystemMessage      string                  `json:"systemMessage,omitempty"`
	SuppressOutput     *bool                   `json:"suppressOutput,omitempty"`
	Decision           string                  `json:"decision,omitempty"`
	Reason             string                  `json:"reason,omitempty"`
	HookSpecificOutput CodexHookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

func CodexAllow() []byte {
	return []byte("{}\n")
}

func CodexBlock(eventName, ruleName, message string) []byte {
	text := "agent-gate: [" + ruleName + "] " + message
	return CodexBlockText(eventName, text)
}

func CodexBlockText(eventName, text string) []byte {
	resp := codexResponse{}

	switch CodexEvent(eventName) {
	case CodexPreToolUse:
		resp.SystemMessage = text
		resp.HookSpecificOutput = CodexHookSpecificOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       "deny",
			PermissionDecisionReason: text,
		}
	case CodexPermissionRequest:
		resp.HookSpecificOutput = CodexHookSpecificOutput{
			HookEventName: "PermissionRequest",
			Decision: CodexPermissionDecision{
				Behavior: "deny",
				Message:  text,
			},
		}
	case CodexPostToolUse:
		resp.Decision = "block"
		resp.Reason = text
	case CodexUserPromptSubmit:
		resp.Decision = "block"
		resp.Reason = text
	case CodexStop:
		resp.Decision = "block"
		resp.Reason = text
	default:
		resp.Decision = "block"
		resp.Reason = text
	}

	bytes, _ := json.Marshal(resp)
	return append(bytes, '\n')
}
