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
