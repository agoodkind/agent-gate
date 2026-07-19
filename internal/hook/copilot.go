package hook

import (
	"bufio"
	"encoding/json"
	"os"
)

func lastCopilotAssistantMessage(transcriptPath string) string {
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()

	var lastMessage string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		if message := copilotAssistantMessageFromLine(scanner.Bytes()); message != "" {
			lastMessage = message
		}
	}
	return lastMessage
}

func copilotAssistantMessageFromLine(line []byte) string {
	var entry struct {
		Type string `json:"type"`
		Data struct {
			Content       string `json:"content"`
			Message       string `json:"message"`
			Text          string `json:"text"`
			ReasoningText string `json:"reasoningText"`
		} `json:"data"`
		Payload struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content string `json:"content"`
			Message string `json:"message"`
			Text    string `json:"text"`
		} `json:"payload"`
		Role    string `json:"role"`
		Content string `json:"content"`
		Message string `json:"message"`
		Text    string `json:"text"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return ""
	}

	if entry.Type == "assistant.message" {
		return firstNonEmpty(entry.Data.Content, entry.Data.Message, entry.Data.Text, entry.Data.ReasoningText)
	}
	if entry.Role == "assistant" {
		return firstNonEmpty(entry.Content, entry.Message, entry.Text)
	}
	if entry.Payload.Role == "assistant" || entry.Payload.Type == "assistant.message" || entry.Payload.Type == "message" {
		return firstNonEmpty(entry.Payload.Content, entry.Payload.Message, entry.Payload.Text)
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type copilotResponse struct {
	AdditionalContext         string          `json:"additionalContext,omitempty"`
	ModifiedArgs              json.RawMessage `json:"modifiedArgs,omitempty"`
	ModifiedResult            json.RawMessage `json:"modifiedResult,omitempty"`
	ModifiedTransformedPrompt string          `json:"modifiedTransformedPrompt,omitempty"`
}

func renderCopilotResponse(request ResponseRequest) Response {
	if request.Decision == ResponseDecisionBlock {
		return renderClaudeResponse(request)
	}
	capability := LookupResponseCapability(SystemCopilot, request.EventName)
	if request.ContextText == "" && request.MutationText == "" {
		return Response{Stdout: ClaudeAllow(), Stderr: nil, ExitCode: 0}
	}
	response := copilotResponse{
		AdditionalContext:         "",
		ModifiedArgs:              nil,
		ModifiedResult:            nil,
		ModifiedTransformedPrompt: "",
	}
	applyCopilotInjection(&response, capability, request.ContextText, request.PromptText)
	applyCopilotMutation(&response, capability, request.ContextText, request.MutationText)
	if response.AdditionalContext == "" && len(response.ModifiedArgs) == 0 && len(response.ModifiedResult) == 0 && response.ModifiedTransformedPrompt == "" {
		return Response{Stdout: ClaudeAllow(), Stderr: nil, ExitCode: 0}
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return Response{Stdout: ClaudeAllow(), Stderr: nil, ExitCode: 0}
	}
	return Response{Stdout: append(encoded, '\n'), Stderr: nil, ExitCode: 0}
}

func applyCopilotInjection(
	response *copilotResponse,
	capability ResponseCapability,
	contextText string,
	promptText string,
) {
	if contextText == "" || !capability.Supports(ResponseCapabilityInject) {
		return
	}
	if capability.Supports(ResponseCapabilityPromptMutation) {
		response.ModifiedTransformedPrompt = prependContext(contextText, promptText)
		return
	}
	response.AdditionalContext = contextText
}

func applyCopilotMutation(
	response *copilotResponse,
	capability ResponseCapability,
	contextText string,
	mutationText string,
) {
	if mutationText == "" {
		return
	}
	if capability.Supports(ResponseCapabilityPromptMutation) {
		response.ModifiedTransformedPrompt = prependContext(contextText, mutationText)
		return
	}
	mutation, ok := validStructuredMutation(mutationText)
	if !ok {
		return
	}
	if capability.Supports(ResponseCapabilityToolInputMutation) {
		response.ModifiedArgs = mutation
		return
	}
	if capability.Supports(ResponseCapabilityToolOutputMutation) {
		toolResult, valid := validCopilotToolOutputMutation(mutationText)
		if valid {
			response.ModifiedResult = toolResult
		}
	}
}

func validCopilotToolOutputMutation(value string) (json.RawMessage, bool) {
	mutation, ok := validStructuredMutation(value)
	if !ok {
		return nil, false
	}
	var result struct {
		ResultType       string  `json:"resultType"`
		TextResultForLLM *string `json:"textResultForLlm"`
	}
	if err := json.Unmarshal(mutation, &result); err != nil {
		return nil, false
	}
	if result.ResultType != "success" || result.TextResultForLLM == nil {
		return nil, false
	}
	return mutation, true
}

func prependContext(contextText string, prompt string) string {
	if contextText == "" {
		return prompt
	}
	if prompt == "" {
		return contextText
	}
	return contextText + "\n\n" + prompt
}
