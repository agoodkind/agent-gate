package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/rules"
)

const codexTranscriptTailBytes int64 = 2 * 1024 * 1024

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
	workdir, err := codexExecWorkdirFromTranscript(
		fields.TranscriptPath,
		fields.TurnID,
		fields.ToolInputCommand,
	)
	if err != nil {
		return fields
	}
	if workdir != "" {
		fields.ToolInputWorkdir = workdir
	}
	return fields
}

// Codex omits the exec workdir from the hook schema.
func codexExecWorkdirFromTranscript(path string, turnID string, command string) (string, error) {
	tail := readCodexTranscriptTail(path)
	lines := strings.Split(string(tail), "\n")
	for _, line := range slices.Backward(lines) {
		workdir, matched, err := codexExecWorkdirFromLine(line, turnID, command)
		if err != nil {
			return "", err
		}
		if matched {
			return workdir, nil
		}
	}
	return "", nil
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

func codexExecWorkdirFromLine(line string, turnID string, command string) (string, bool, error) {
	if strings.TrimSpace(line) == "" {
		return "", false, nil
	}
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
		slog.Warn("decode Codex exec transcript line failed", "err", err)
		return "", false, fmt.Errorf("decode Codex exec transcript line: %w", err)
	}
	if entry.Type != "response_item" || entry.Payload.Type != "custom_tool_call" {
		return "", false, nil
	}
	if entry.Payload.Name != "exec" || entry.Payload.Meta.TurnID != turnID {
		return "", false, nil
	}
	return codexExecWorkdirFromInput(entry.Payload.Input, command)
}
