package hook

// UserPrompt returns user-authored text only for typed prompt submission
// events. Tool prompt fields and assistant output events are not observations.
func UserPrompt(payload Payload) (string, bool) {
	var prompt string
	switch event := payload.Event.(type) {
	case ClaudeUserPromptSubmitPayload:
		prompt = event.Prompt
	case CodexUserPromptSubmitPayload:
		prompt = event.Prompt
	case CursorBeforeSubmitPromptPayload:
		prompt = event.Prompt
	case CopilotPayload:
		if event.EventName() != "userPromptTransformed" {
			return "", false
		}
		prompt = event.Prompt
	case GeminiBeforeAgentPayload:
		prompt = event.Prompt
	default:
		return "", false
	}
	if prompt == "" {
		return "", false
	}
	return prompt, true
}
