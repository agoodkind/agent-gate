package hook

import "strings"

func NormalizeVSCodePayload(raw RawPayload) RawPayload {
	return normalizeVSCodeToolInput(raw)
}

func normalizeVSCodeToolInput(raw RawPayload) RawPayload {
	ti, ok := raw["tool_input"].(map[string]any)
	if !ok {
		return raw
	}

	normalized := clonePayload(raw)
	normalizedToolInput := cloneMap(ti)
	mirrorStringKey(normalizedToolInput, "filePath", "file_path")
	mirrorStringKey(normalizedToolInput, "oldString", "old_string")
	mirrorStringKey(normalizedToolInput, "newString", "new_string")
	normalizeVSCodeReplacements(normalized, normalizedToolInput)
	normalized["tool_input"] = normalizedToolInput
	return normalized
}

func normalizeVSCodeReplacements(raw RawPayload, toolInput map[string]any) {
	replacements, ok := toolInput["replacements"].([]any)
	if !ok {
		return
	}

	normalizedReplacements := make([]any, 0, len(replacements))
	edits := make([]any, 0, len(replacements))
	oldStrings := make([]string, 0, len(replacements))
	newStrings := make([]string, 0, len(replacements))
	for _, replacement := range replacements {
		replacementMap, ok := replacement.(map[string]any)
		if !ok {
			normalizedReplacements = append(normalizedReplacements, replacement)
			continue
		}
		normalizedReplacement := cloneMap(replacementMap)
		mirrorStringKey(normalizedReplacement, "filePath", "file_path")
		mirrorStringKey(normalizedReplacement, "oldString", "old_string")
		mirrorStringKey(normalizedReplacement, "newString", "new_string")
		normalizedReplacements = append(normalizedReplacements, normalizedReplacement)
		edit := normalizedVSCodeEdit(normalizedReplacement, &oldStrings, &newStrings)
		if len(edit) > 0 {
			edits = append(edits, edit)
		}
	}

	toolInput["replacements"] = normalizedReplacements
	if _, ok := raw["edits"]; !ok && len(edits) > 0 {
		raw["edits"] = edits
	}
	if _, ok := toolInput["old_string"].(string); !ok && len(oldStrings) > 0 {
		toolInput["old_string"] = strings.Join(oldStrings, "\n")
	}
	if _, ok := toolInput["new_string"].(string); !ok && len(newStrings) > 0 {
		toolInput["new_string"] = strings.Join(newStrings, "\n")
	}
}

func normalizedVSCodeEdit(replacement map[string]any, oldStrings *[]string, newStrings *[]string) map[string]any {
	edit := map[string]any{}
	if filePath, ok := replacement["file_path"].(string); ok {
		edit["file_path"] = filePath
	}
	if oldString, ok := replacement["old_string"].(string); ok {
		edit["old_string"] = oldString
		*oldStrings = append(*oldStrings, oldString)
	}
	if newString, ok := replacement["new_string"].(string); ok {
		edit["new_string"] = newString
		*newStrings = append(*newStrings, newString)
	}
	return edit
}

func mirrorStringKey(values map[string]any, fromKey string, toKey string) {
	if _, ok := values[toKey].(string); ok {
		return
	}
	if value, ok := values[fromKey].(string); ok {
		values[toKey] = value
	}
}
