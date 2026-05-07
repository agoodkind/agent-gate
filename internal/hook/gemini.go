package hook

import "encoding/json"

// GeminiEvent enumerates the documented Gemini CLI hook event names.
type GeminiEvent string

// GeminiEvent variants. Each constant is a literal Gemini hook event name.
const (
	// GeminiBeforeTool fires before a tool is invoked.
	GeminiBeforeTool GeminiEvent = "BeforeTool"
	// GeminiAfterTool fires after a tool returns.
	GeminiAfterTool GeminiEvent = "AfterTool"
	// GeminiBeforeAgent fires before the agent processes a turn.
	GeminiBeforeAgent GeminiEvent = "BeforeAgent"
	// GeminiAfterAgent fires after the agent completes a turn.
	GeminiAfterAgent GeminiEvent = "AfterAgent"
	// GeminiBeforeModel fires before a model request is dispatched.
	GeminiBeforeModel GeminiEvent = "BeforeModel"
	// GeminiBeforeToolSelection fires before tool selection runs.
	GeminiBeforeToolSelection GeminiEvent = "BeforeToolSelection"
	// GeminiAfterModel fires after a model request returns.
	GeminiAfterModel GeminiEvent = "AfterModel"
	// GeminiSessionStart fires when a Gemini session starts.
	GeminiSessionStart GeminiEvent = "SessionStart"
	// GeminiSessionEnd fires when a Gemini session ends.
	GeminiSessionEnd GeminiEvent = "SessionEnd"
	// GeminiNotification fires for ad-hoc notifications.
	GeminiNotification GeminiEvent = "Notification"
	// GeminiPreCompress fires before context compression starts.
	GeminiPreCompress GeminiEvent = "PreCompress"
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
	case GeminiBeforeToolSelection,
		GeminiSessionStart,
		GeminiSessionEnd,
		GeminiNotification,
		GeminiPreCompress:
		return false
	}
	return false
}

type geminiResponse struct {
	SystemMessage  string `json:"systemMessage,omitempty"`
	SuppressOutput *bool  `json:"suppressOutput,omitempty"`
	Continue       *bool  `json:"continue,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	Decision       string `json:"decision,omitempty"`
	Reason         string `json:"reason,omitempty"`
}

// GeminiAllow returns the stdout bytes for an allow response.
func GeminiAllow() []byte {
	return []byte("{}\n")
}

// GeminiBlock returns the stdout bytes for a deny response, formatting the
// agent-gate rule name plus message as the reason.
func GeminiBlock(eventName, ruleName, message string) []byte {
	text := "agent-gate: [" + ruleName + "] " + message
	return GeminiBlockText(eventName, text)
}

// GeminiBlockText returns the stdout bytes for a deny response carrying the
// given free-form text. Events that Gemini cannot block are silently
// reduced to an empty response.
func GeminiBlockText(eventName, text string) []byte {
	resp := geminiResponse{
		SystemMessage:  "",
		SuppressOutput: nil,
		Continue:       nil,
		StopReason:     "",
		Decision:       "deny",
		Reason:         text,
	}

	switch GeminiEvent(eventName) {
	case GeminiAfterAgent,
		GeminiAfterTool,
		GeminiBeforeTool,
		GeminiBeforeAgent,
		GeminiBeforeModel,
		GeminiAfterModel:
		resp.Reason = text
	case GeminiBeforeToolSelection,
		GeminiSessionStart,
		GeminiSessionEnd,
		GeminiNotification,
		GeminiPreCompress:
		resp.Decision = ""
		resp.Reason = ""
	default:
		resp.Decision = ""
		resp.Reason = ""
	}

	bytes, err := json.Marshal(resp)
	if err != nil {
		return []byte("{}\n")
	}
	return append(bytes, '\n')
}

func renderGeminiResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return Response{
			Stdout:   GeminiBlockText(request.EventName, request.DiagnosticText),
			Stderr:   nil,
			ExitCode: 0,
		}
	}
	return Response{Stdout: GeminiAllow(), Stderr: nil, ExitCode: 0}
}
