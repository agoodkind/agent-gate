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

type codexResponse struct {
	Continue           *bool          `json:"continue,omitempty"`
	StopReason         string         `json:"stopReason,omitempty"`
	SystemMessage      string         `json:"systemMessage,omitempty"`
	SuppressOutput     *bool          `json:"suppressOutput,omitempty"`
	Decision           string         `json:"decision,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	HookSpecificOutput map[string]any `json:"hookSpecificOutput,omitempty"`
}

func CodexAllow() []byte {
	return []byte("{}\n")
}

func CodexBlock(eventName, ruleName, message string) []byte {
	text := "agent-gate: [" + ruleName + "] " + message
	resp := codexResponse{}

	switch CodexEvent(eventName) {
	case CodexPreToolUse:
		resp.SystemMessage = text
		resp.HookSpecificOutput = map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       "deny",
			"permissionDecisionReason": text,
		}
	case CodexPermissionRequest:
		resp.HookSpecificOutput = map[string]any{
			"hookEventName": "PermissionRequest",
			"decision": map[string]any{
				"behavior": "deny",
				"message":  text,
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

	b, _ := json.Marshal(resp)
	return append(b, '\n')
}
