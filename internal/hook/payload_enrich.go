package hook

import (
	"bufio"
	"encoding/json"
	"os"
)

// EnrichPayload normalizes cross-tool execution context into effective_cwd.
//
// Some hook providers report the chat/session cwd at the top level while the
// actual shell operation carries its own working directory elsewhere, such as
// tool_input.workdir or a matching function-call entry in a transcript. Rules
// should be able to reason about the operation cwd without knowing every
// provider-specific envelope shape, so this is intentionally best-effort and
// leaves the original cwd intact.
func EnrichPayload(raw RawPayload) RawPayload {
	if raw == nil {
		return raw
	}
	if strField(raw, "effective_cwd") != "" {
		return raw
	}

	if cwd := payloadOperationCwd(raw); cwd != "" {
		return withEffectiveCwd(raw, cwd)
	}
	if cwd := transcriptOperationCwd(raw); cwd != "" {
		enriched := withEffectiveCwd(raw, cwd)
		return withToolInputWorkdir(enriched, cwd)
	}
	return raw
}

func payloadOperationCwd(raw RawPayload) string {
	if ti, ok := raw["tool_input"].(map[string]any); ok {
		for _, key := range []string{"workdir", "working_directory", "cwd", "directory"} {
			if v := strFromMap(ti, key); v != "" {
				return v
			}
		}
	}
	for _, key := range []string{"workdir", "working_directory", "directory"} {
		if v := strField(raw, key); v != "" {
			return v
		}
	}
	return ""
}

func withEffectiveCwd(raw RawPayload, cwd string) RawPayload {
	enriched := clonePayload(raw)
	enriched["effective_cwd"] = cwd
	return enriched
}

func withToolInputWorkdir(raw RawPayload, cwd string) RawPayload {
	enriched := clonePayload(raw)
	ti, _ := enriched["tool_input"].(map[string]any)
	if ti == nil {
		ti = map[string]any{}
	} else {
		ti = cloneMap(ti)
	}
	if _, ok := ti["workdir"].(string); !ok {
		ti["workdir"] = cwd
	}
	enriched["tool_input"] = ti
	return enriched
}

func clonePayload(raw RawPayload) RawPayload {
	out := make(RawPayload, len(raw)+1)
	for k, v := range raw {
		out[k] = v
	}
	return out
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func transcriptOperationCwd(raw RawPayload) string {
	transcriptPath := strField(raw, "transcript_path")
	toolUseID := strField(raw, "tool_use_id")
	if transcriptPath == "" || toolUseID == "" {
		return ""
	}

	f, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		if cwd := transcriptLineOperationCwd(scanner.Bytes(), toolUseID); cwd != "" {
			return cwd
		}
	}
	return ""
}

func transcriptLineOperationCwd(line []byte, toolUseID string) string {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string `json:"type"`
			Name      string `json:"name"`
			CallID    string `json:"call_id"`
			Arguments string `json:"arguments"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return ""
	}
	if entry.Type != "response_item" || entry.Payload.Type != "function_call" {
		return ""
	}
	if entry.Payload.CallID != toolUseID || entry.Payload.Arguments == "" {
		return ""
	}

	var args map[string]any
	if err := json.Unmarshal([]byte(entry.Payload.Arguments), &args); err != nil {
		return ""
	}
	for _, key := range []string{"workdir", "working_directory", "cwd", "directory"} {
		if v, _ := args[key].(string); v != "" {
			return v
		}
	}
	return ""
}
