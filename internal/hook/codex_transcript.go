package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/rules"
)

const codexTranscriptTailBytes int64 = 2 * 1024 * 1024

var (
	codexExecCommandPattern = regexp.MustCompile(`\bcmd\s*:\s*("(?:\\.|[^"\\])*")`)
	codexExecWorkdirPattern = regexp.MustCompile(`\bworkdir\s*:\s*("(?:\\.|[^"\\])*")`)
)

func recoverCodexExecWorkdir(system System, fields rules.FieldSet) rules.FieldSet {
	if system != SystemCodex || fields.ToolInputWorkdir != "" {
		return fields
	}
	if !strings.EqualFold(fields.ToolName, "bash") || fields.ToolInputCommand == "" {
		return fields
	}
	if fields.TranscriptPath == "" || fields.TurnID == "" {
		return fields
	}
	if workdir := codexExecWorkdirFromTranscript(
		fields.TranscriptPath,
		fields.TurnID,
		fields.ToolInputCommand,
	); workdir != "" {
		fields.ToolInputWorkdir = workdir
	}
	return fields
}

func codexExecWorkdirFromTranscript(path string, turnID string, command string) string {
	tail := readCodexTranscriptTail(path)
	lines := strings.Split(string(tail), "\n")
	for _, line := range slices.Backward(lines) {
		if workdir := codexExecWorkdirFromLine(line, turnID, command); workdir != "" {
			return workdir
		}
	}
	return ""
}

func readCodexTranscriptTail(path string) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil
	}
	offset := max(info.Size()-codexTranscriptTailBytes, 0)
	content := make([]byte, info.Size()-offset)
	count, err := file.ReadAt(content, offset)
	if err != nil && count == 0 {
		return nil
	}
	content = content[:count]
	if offset > 0 {
		if newline := bytes.IndexByte(content, '\n'); newline >= 0 {
			content = content[newline+1:]
		}
	}
	return content
}

func codexExecWorkdirFromLine(line string, turnID string, command string) string {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Type  string `json:"type"`
			Name  string `json:"name"`
			Input string `json:"input"`
			Meta  struct {
				TurnID string `json:"turn_id"`
			} `json:"internal_chat_message_metadata_passthrough"`
		} `json:"payload"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return ""
	}
	if entry.Type != "response_item" || entry.Payload.Type != "custom_tool_call" {
		return ""
	}
	if entry.Payload.Name != "exec" || entry.Payload.Meta.TurnID != turnID {
		return ""
	}
	if unquoteCodexExecField(entry.Payload.Input, codexExecCommandPattern) != command {
		return ""
	}
	return unquoteCodexExecField(entry.Payload.Input, codexExecWorkdirPattern)
}

func unquoteCodexExecField(input string, pattern *regexp.Regexp) string {
	match := pattern.FindStringSubmatch(input)
	if len(match) != 2 {
		return ""
	}
	var value string
	if err := json.Unmarshal([]byte(match[1]), &value); err != nil {
		return ""
	}
	return value
}
