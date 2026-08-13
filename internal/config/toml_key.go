package config

import (
	"strconv"
	"strings"
)

type tomlMultilineString byte

const (
	tomlMultilineNone    tomlMultilineString = 0
	tomlMultilineBasic   tomlMultilineString = '"'
	tomlMultilineLiteral tomlMultilineString = '\''
)

func tomlStructuralLines(lines []string) []bool {
	structural := make([]bool, len(lines))
	state := tomlMultilineNone
	for i := range lines {
		structural[i] = state == tomlMultilineNone
		state = advanceTOMLMultilineState(lines[i], state)
	}
	return structural
}

func advanceTOMLMultilineState(line string, state tomlMultilineString) tomlMultilineString {
	var quote byte
	escaped := false
	for i := 0; i < len(line); i++ {
		if state != tomlMultilineNone {
			delimiter := string([]byte{byte(state), byte(state), byte(state)})
			closing := strings.Index(line[i:], delimiter)
			if closing < 0 {
				return state
			}
			closing += i
			if state == tomlMultilineBasic && tomlByteEscaped(line, closing) {
				i = closing
				continue
			}
			state = tomlMultilineNone
			i = closing + len(delimiter) - 1
			continue
		}
		current := line[i]
		if quote != 0 {
			if quote == '"' && current == '\\' && !escaped {
				escaped = true
				continue
			}
			if current == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if current == '#' {
			return state
		}
		if current != '"' && current != '\'' {
			continue
		}
		if i+2 < len(line) && line[i+1] == current && line[i+2] == current {
			state = tomlMultilineString(current)
			i += 2
			continue
		}
		quote = current
	}
	return state
}

func tomlMultilineClosingIndex(line string, state tomlMultilineString) int {
	if state == tomlMultilineNone {
		return -1
	}
	delimiter := string([]byte{byte(state), byte(state), byte(state)})
	searchStart := 0
	for searchStart < len(line) {
		closing := strings.Index(line[searchStart:], delimiter)
		if closing < 0 {
			return -1
		}
		closing += searchStart
		if state != tomlMultilineBasic || !tomlByteEscaped(line, closing) {
			return closing
		}
		searchStart = closing + 1
	}
	return -1
}

func tomlByteEscaped(source string, index int) bool {
	backslashes := 0
	for i := index - 1; i >= 0 && source[i] == '\\'; i-- {
		backslashes++
	}
	return backslashes%2 == 1
}

func tomlTableHeaderPath(line string) ([]string, bool) {
	path, array, ok := tomlTableContextPath(line)
	return path, ok && !array
}

func tomlTableContextPath(line string) ([]string, bool, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return nil, false, false
	}
	array := strings.HasPrefix(trimmed, "[[")
	openingLength := 1
	if array {
		openingLength = 2
	}
	closing := tomlUnquotedIndex(trimmed, ']')
	if closing < 0 {
		return nil, false, false
	}
	closingLength := 1
	if array {
		if closing+1 >= len(trimmed) || trimmed[closing+1] != ']' {
			return nil, false, false
		}
		closingLength = 2
	}
	rest := strings.TrimSpace(trimmed[closing+closingLength:])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return nil, false, false
	}
	path, ok := parseTOMLKeyPath(trimmed[openingLength:closing])
	return path, array, ok
}

func tomlAssignmentKeyPath(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return nil, false
	}
	equals := tomlUnquotedIndex(trimmed, '=')
	if equals < 0 {
		return nil, false
	}
	return parseTOMLKeyPath(trimmed[:equals])
}

func parseTOMLKeyPath(source string) ([]string, bool) {
	remaining := strings.TrimSpace(source)
	if remaining == "" {
		return nil, false
	}
	var path []string
	for remaining != "" {
		key, rest, ok := parseTOMLKeyPart(remaining)
		if !ok {
			return nil, false
		}
		path = append(path, key)
		remaining = strings.TrimSpace(rest)
		if remaining == "" {
			return path, true
		}
		if remaining[0] != '.' {
			return nil, false
		}
		remaining = strings.TrimSpace(remaining[1:])
		if remaining == "" {
			return nil, false
		}
	}
	return path, true
}

func parseTOMLKeyPart(source string) (string, string, bool) {
	if source[0] == '"' || source[0] == '\'' {
		return parseTOMLQuotedKeyPart(source)
	}
	end := 0
	for end < len(source) && isTOMLBareKeyByte(source[end]) {
		end++
	}
	if end == 0 {
		return "", source, false
	}
	return source[:end], source[end:], true
}

func parseTOMLQuotedKeyPart(source string) (string, string, bool) {
	quote := source[0]
	escaped := false
	for i := range len(source) - 1 {
		i++
		if quote == '"' && source[i] == '\\' && !escaped {
			escaped = true
			continue
		}
		if source[i] == quote && !escaped {
			raw := source[:i+1]
			if quote == '\'' {
				return raw[1 : len(raw)-1], source[i+1:], true
			}
			key, err := strconv.Unquote(raw)
			if err != nil {
				return "", source, false
			}
			return key, source[i+1:], true
		}
		escaped = false
	}
	return "", source, false
}

func tomlUnquotedIndex(source string, target byte) int {
	var quote byte
	escaped := false
	for i := range len(source) {
		current := source[i]
		if quote != 0 {
			if quote == '"' && current == '\\' && !escaped {
				escaped = true
				continue
			}
			if current == quote && !escaped {
				quote = 0
			}
			escaped = false
			continue
		}
		if current == '"' || current == '\'' {
			quote = current
			continue
		}
		if current == target {
			return i
		}
	}
	return -1
}

func hasTOMLKeyPrefix(path []string, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for i := range prefix {
		if path[i] != prefix[i] {
			return false
		}
	}
	return true
}

func isTOMLBareKeyByte(value byte) bool {
	return value == '_' || value == '-' ||
		(value >= 'A' && value <= 'Z') ||
		(value >= 'a' && value <= 'z') ||
		(value >= '0' && value <= '9')
}
