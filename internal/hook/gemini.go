package hook

import "encoding/json"

// GeminiEvent enumerates the documented Gemini CLI hook event names.
type GeminiEvent string

const (
	GeminiBeforeTool          GeminiEvent = "BeforeTool"
	GeminiAfterTool           GeminiEvent = "AfterTool"
	GeminiBeforeAgent         GeminiEvent = "BeforeAgent"
	GeminiAfterAgent          GeminiEvent = "AfterAgent"
	GeminiBeforeModel         GeminiEvent = "BeforeModel"
	GeminiBeforeToolSelection GeminiEvent = "BeforeToolSelection"
	GeminiAfterModel          GeminiEvent = "AfterModel"
	GeminiSessionStart        GeminiEvent = "SessionStart"
	GeminiSessionEnd          GeminiEvent = "SessionEnd"
	GeminiNotification        GeminiEvent = "Notification"
	GeminiPreCompress         GeminiEvent = "PreCompress"
)

// CanBlockGemini returns true for Gemini events where a deny/stop response
// changes the tool, model, or turn lifecycle.
func CanBlockGemini(eventName string) bool {
	switch GeminiEvent(eventName) {
	case GeminiBeforeTool,
		GeminiAfterTool,
		GeminiBeforeAgent,
		GeminiAfterAgent,
		GeminiBeforeModel,
		GeminiAfterModel:
		return true
	}
	return false
}

type geminiResponse struct {
	SystemMessage      string         `json:"systemMessage,omitempty"`
	SuppressOutput     *bool          `json:"suppressOutput,omitempty"`
	Continue           *bool          `json:"continue,omitempty"`
	StopReason         string         `json:"stopReason,omitempty"`
	Decision           string         `json:"decision,omitempty"`
	Reason             string         `json:"reason,omitempty"`
	HookSpecificOutput map[string]any `json:"hookSpecificOutput,omitempty"`
}

func GeminiAllow() []byte {
	return []byte("{}\n")
}

func GeminiBlock(eventName, ruleName, message string) []byte {
	text := "agent-gate: [" + ruleName + "] " + message
	resp := geminiResponse{
		Decision: "deny",
		Reason:   text,
	}

	switch GeminiEvent(eventName) {
	case GeminiAfterAgent:
		resp.Reason = text
	case GeminiAfterTool:
		resp.Reason = text
	case GeminiBeforeTool:
		resp.Reason = text
	case GeminiBeforeAgent:
		resp.Reason = text
	case GeminiBeforeModel:
		resp.Reason = text
	case GeminiAfterModel:
		resp.Reason = text
	default:
		resp.Decision = ""
		resp.Reason = ""
	}

	b, _ := json.Marshal(resp)
	return append(b, '\n')
}
