package hook

import "log/slog"

// claudeLogAttrs extracts slog attributes from a Claude payload.
func claudeLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("tool_use_id", strField(raw, "tool_use_id")),
		slog.String("source", strField(raw, "source")),
		slog.String("file_path", strField(raw, "file_path")),
		slog.String("model", strField(raw, "model")),
		slog.String("permission_mode", strField(raw, "permission_mode")),
		slog.String("agent_id", strField(raw, "agent_id")),
		slog.String("agent_type", strField(raw, "agent_type")),
	}

	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_description", truncate(strFromMap(ti, "description"), 200)),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
			slog.String("ti_pattern", strFromMap(ti, "pattern")),
			slog.String("ti_url", strFromMap(ti, "url")),
			slog.String("ti_query", strFromMap(ti, "query")),
			slog.String("ti_prompt_snippet", truncate(strFromMap(ti, "prompt"), 200)),
		)
	}

	for _, key := range []string{
		"prompt", "message", "error", "error_type", "reason",
		"change_type", "trigger", "memory_type", "load_reason",
		"notification_type", "task_id", "task_subject",
		"new_cwd", "previous_cwd", "mcp_server_name",
	} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key, truncate(v, 200)))
		}
	}

	if v := strField(raw, "last_assistant_message"); v != "" {
		attrs = append(attrs, slog.String("last_assistant_message_snippet", truncate(v, 200)))
	}

	return attrs
}

// cursorLogAttrs extracts slog attributes from a Cursor payload.
func cursorLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("conversation_id", strField(raw, "conversation_id")),
		slog.String("generation_id", strField(raw, "generation_id")),
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("file_path", strField(raw, "file_path")),
		slog.String("command", strField(raw, "command")),
	}

	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
		)
	}

	// afterFileEdit sends edits as an array; log the first new_string snippet.
	if edits, ok := raw["edits"].([]any); ok && len(edits) > 0 {
		if edit, ok := edits[0].(map[string]any); ok {
			attrs = append(attrs,
				slog.String("edit0_old_snippet", truncate(strFromMap(edit, "old_string"), 200)),
				slog.String("edit0_new_snippet", truncate(strFromMap(edit, "new_string"), 200)),
			)
		}
	}

	for _, key := range []string{"text", "prompt", "assistant_message", "last_assistant_message"} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key+"_snippet", truncate(v, 200)))
		}
	}

	return attrs
}

func codexLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("tool_use_id", strField(raw, "tool_use_id")),
		slog.String("turn_id", strField(raw, "turn_id")),
		slog.String("source", strField(raw, "source")),
		slog.String("model", strField(raw, "model")),
	}

	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_description", truncate(strFromMap(ti, "description"), 200)),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
			slog.String("ti_old_string_snippet", truncate(strFromMap(ti, "old_string"), 200)),
			slog.String("ti_new_string_snippet", truncate(strFromMap(ti, "new_string"), 200)),
			slog.String("ti_pattern", strFromMap(ti, "pattern")),
			slog.String("ti_url", strFromMap(ti, "url")),
			slog.String("ti_query", strFromMap(ti, "query")),
			slog.String("ti_prompt_snippet", truncate(strFromMap(ti, "prompt"), 200)),
		)
	}

	for _, key := range []string{"prompt", "stopReason", "reason", "notification_type"} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key, truncate(v, 200)))
		}
	}

	if v := strField(raw, "last_assistant_message"); v != "" {
		attrs = append(attrs, slog.String("last_assistant_message_snippet", truncate(v, 200)))
	}

	return attrs
}

func geminiLogAttrs(raw RawPayload) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("tool_name", strField(raw, "tool_name")),
		slog.String("source", strField(raw, "source")),
		slog.String("timestamp", strField(raw, "timestamp")),
		slog.String("notification_type", strField(raw, "notification_type")),
	}

	if ti, ok := raw["tool_input"].(map[string]any); ok {
		attrs = append(attrs,
			slog.String("ti_command", strFromMap(ti, "command")),
			slog.String("ti_file_path", strFromMap(ti, "file_path")),
			slog.String("ti_description", truncate(strFromMap(ti, "description"), 200)),
			slog.String("ti_content_snippet", truncate(strFromMap(ti, "content"), 200)),
		)
	}

	for _, key := range []string{"prompt", "prompt_response", "message", "reason", "original_request_name"} {
		if v := strField(raw, key); v != "" {
			attrs = append(attrs, slog.String(key+"_snippet", truncate(v, 200)))
		}
	}

	return attrs
}

// strField extracts a string from a RawPayload, returning "" if missing.
func strField(p RawPayload, key string) string {
	v, _ := p[key].(string)
	return v
}

// strFromMap extracts a string from a nested map, returning "" if missing.
func strFromMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// truncate returns s shortened to at most n runes, appending "…" if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "\u2026"
}
