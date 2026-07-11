package shellparse

import (
	"slices"
	"strings"
)

// ExpandEnvironmentVariables substitutes known simple shell variables while
// preserving quoting semantics for later structural parsing.
func ExpandEnvironmentVariables(command string, getenv func(string) string) string {
	if command == "" || getenv == nil {
		return command
	}
	var builder strings.Builder
	quote := byte(0)
	for index := 0; index < len(command); {
		character := command[index]
		switch character {
		case '\\':
			if quote != '\'' {
				index = writeEscapedEnvironmentCharacter(&builder, command, index)
				continue
			}
		case '\'':
			quote = transitionEnvironmentQuote(quote, character)
		case '"':
			if quote != '\'' {
				quote = transitionEnvironmentQuote(quote, character)
			}
		case '$':
			if quote != '\'' {
				if nextIndex, written := writeEnvironmentReference(
					&builder, command, index, quote, getenv,
				); written {
					index = nextIndex
					continue
				}
			}
		}
		builder.WriteByte(character)
		index++
	}
	return builder.String()
}

// ReferencedEnvironmentVariables returns the distinct variables that the
// structural expander can safely resolve.
func ReferencedEnvironmentVariables(command string) []string {
	names := make(map[string]bool)
	quote := byte(0)
	for index := 0; index < len(command); {
		character := command[index]
		if character == '\\' && quote != '\'' {
			index += min(2, len(command)-index)
			continue
		}
		if character == '\'' || character == '"' && quote != '\'' {
			quote = transitionEnvironmentQuote(quote, character)
			index++
			continue
		}
		if character == '$' && quote == '"' {
			name, nextIndex, found := environmentReference(command, index)
			if found {
				names[name] = true
				index = nextIndex
				continue
			}
		}
		index++
	}
	result := make([]string, 0, len(names))
	for name := range names {
		result = append(result, name)
	}
	slices.Sort(result)
	return result
}

func writeEscapedEnvironmentCharacter(
	builder *strings.Builder,
	command string,
	index int,
) int {
	builder.WriteByte(command[index])
	index++
	if index < len(command) {
		builder.WriteByte(command[index])
		index++
	}
	return index
}

func transitionEnvironmentQuote(current byte, candidate byte) byte {
	switch current {
	case 0:
		return candidate
	case candidate:
		return 0
	default:
		return current
	}
}

func writeEnvironmentReference(
	builder *strings.Builder,
	command string,
	index int,
	quote byte,
	getenv func(string) string,
) (int, bool) {
	if quote != '"' {
		return index, false
	}
	name, nextIndex, found := environmentReference(command, index)
	if !found {
		return index, false
	}
	value := getenv(name)
	if value == "" {
		builder.WriteString(command[index:nextIndex])
		return nextIndex, true
	}
	builder.WriteString(escapeDoubleQuotedEnvironment(value))
	return nextIndex, true
}

func environmentReference(command string, index int) (string, int, bool) {
	nameStart := index + 1
	if len(command) <= nameStart {
		return "", index, false
	}
	if command[nameStart] == '{' {
		nameStart++
		nameEnd := nameStart
		for nameEnd < len(command) && literalAssignmentNamePart(command[nameEnd]) {
			nameEnd++
		}
		if nameEnd == nameStart || len(command) <= nameEnd || command[nameEnd] != '}' {
			return "", index, false
		}
		return command[nameStart:nameEnd], nameEnd + 1, true
	}
	if !literalAssignmentNameStart(command[nameStart]) {
		return "", index, false
	}
	nameEnd := nameStart + 1
	for nameEnd < len(command) && literalAssignmentNamePart(command[nameEnd]) {
		nameEnd++
	}
	return command[nameStart:nameEnd], nameEnd, true
}

func escapeDoubleQuotedEnvironment(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
	)
	return replacer.Replace(value)
}
