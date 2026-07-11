package oracle

import (
	"regexp"
	"strings"
)

var literalAssignmentRefPattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

func expandLiteralAssignments(command string) string {
	values := literalAssignmentValues(command)
	if len(values) == 0 {
		return command
	}

	var builder strings.Builder
	segmentStart := 0
	index := 0
	for index < len(command) {
		if command[index] != '\'' {
			index++
			continue
		}
		builder.WriteString(substituteLiteralAssignmentRefs(command[segmentStart:index], values))
		nextIndex, ok := skipLiteralAssignmentQuote(command, index)
		if !ok {
			builder.WriteString(command[index:])
			return builder.String()
		}
		builder.WriteString(command[index:nextIndex])
		index = nextIndex
		segmentStart = index
	}
	builder.WriteString(substituteLiteralAssignmentRefs(command[segmentStart:], values))
	return builder.String()
}

func substituteLiteralAssignmentRefs(segment string, values map[string]string) string {
	return literalAssignmentRefPattern.ReplaceAllStringFunc(segment, func(match string) string {
		name := variableName(match)
		value, found := values[name]
		if !found {
			return match
		}
		return value
	})
}

func literalAssignmentValues(command string) map[string]string {
	values := make(map[string]string)
	counts := make(map[string]int)

	index := 0
	for index < len(command) {
		for {
			index = skipLiteralAssignmentSpace(command, index)
			nextIndex, found := readLiteralAssignment(command, index, values, counts)
			if !found {
				break
			}
			index = nextIndex
		}

		nextIndex, found := nextLiteralAssignmentCommandStart(command, index)
		if !found {
			break
		}
		index = nextIndex
	}

	for name, count := range counts {
		if 1 < count {
			delete(values, name)
		}
	}
	return values
}

func readLiteralAssignment(
	command string,
	index int,
	values map[string]string,
	counts map[string]int,
) (int, bool) {
	name, valueStart, ok := readLiteralAssignmentName(command, index)
	if !ok {
		return index, false
	}
	value, nextIndex, ok := readLiteralAssignmentValue(command, valueStart)
	if !ok {
		return index, false
	}

	counts[name]++
	if literalAssignmentValueIsSafe(value) {
		values[name] = stripOuterQuotes(value)
	}
	return nextIndex, true
}

func readLiteralAssignmentName(command string, index int) (string, int, bool) {
	if len(command) <= index || !literalAssignmentNameStart(command[index]) {
		return "", index, false
	}

	nameEnd := index + 1
	for nameEnd < len(command) && literalAssignmentNamePart(command[nameEnd]) {
		nameEnd++
	}
	if len(command) <= nameEnd || command[nameEnd] != '=' {
		return "", index, false
	}
	return command[index:nameEnd], nameEnd + 1, true
}

func readLiteralAssignmentValue(command string, index int) (string, int, bool) {
	if len(command) <= index {
		return "", index, true
	}

	if command[index] == '\'' || command[index] == '"' {
		quote := command[index]
		valueEnd := index + 1
		for valueEnd < len(command) {
			if command[valueEnd] == quote {
				nextIndex := valueEnd + 1
				if nextIndex < len(command) && !literalAssignmentWordBoundary(command, nextIndex) {
					return "", index, false
				}
				return command[index:nextIndex], nextIndex, true
			}
			valueEnd++
		}
		return "", index, false
	}

	valueEnd := index
	for valueEnd < len(command) && !literalAssignmentWordBoundary(command, valueEnd) {
		valueEnd++
	}
	return command[index:valueEnd], valueEnd, true
}

func literalAssignmentValueIsSafe(value string) bool {
	if value == "" {
		return true
	}

	unquoted := stripOuterQuotes(value)
	if strings.ContainsAny(unquoted, "$`) \t\n*?[{") {
		return false
	}
	if strings.Contains(unquoted, "\\") {
		return false
	}
	return !strings.Contains(unquoted, "}")
}

func skipLiteralAssignmentSpace(command string, index int) int {
	for index < len(command) && (command[index] == ' ' || command[index] == '\t') {
		index++
	}
	return index
}

func nextLiteralAssignmentCommandStart(command string, index int) (int, bool) {
	for index < len(command) {
		switch command[index] {
		case '\'', '"':
			nextIndex, ok := skipLiteralAssignmentQuote(command, index)
			if !ok {
				return len(command), false
			}
			index = nextIndex
		case ';', '\n':
			return index + 1, true
		case '&':
			if index+1 < len(command) && command[index+1] == '&' {
				return index + 2, true
			}
			index++
		case '|':
			if index+1 < len(command) && command[index+1] == '|' {
				return index + 2, true
			}
			index++
		default:
			index++
		}
	}
	return len(command), false
}

func skipLiteralAssignmentQuote(command string, index int) (int, bool) {
	quote := command[index]
	index++
	for index < len(command) {
		if command[index] == quote {
			return index + 1, true
		}
		index++
	}
	return index, false
}

func literalAssignmentWordBoundary(command string, index int) bool {
	if command[index] == ' ' || command[index] == '\t' || command[index] == '\n' {
		return true
	}
	if command[index] == ';' {
		return true
	}
	if command[index] == '&' && index+1 < len(command) && command[index+1] == '&' {
		return true
	}
	return command[index] == '|' && index+1 < len(command) && command[index+1] == '|'
}

func literalAssignmentNameStart(char byte) bool {
	return betweenLiteralAssignmentChars(char, 'A', 'Z') ||
		betweenLiteralAssignmentChars(char, 'a', 'z') || char == '_'
}

func literalAssignmentNamePart(char byte) bool {
	return literalAssignmentNameStart(char) || betweenLiteralAssignmentChars(char, '0', '9')
}

func betweenLiteralAssignmentChars(char byte, first byte, last byte) bool {
	return first <= char && char <= last
}
