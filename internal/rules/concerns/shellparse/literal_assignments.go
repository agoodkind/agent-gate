// Package shellparse contains generic preprocessing shared by shell concerns.
package shellparse

import (
	"regexp"
	"strings"
)

var literalAssignmentRefPattern = regexp.MustCompile(
	`\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))`,
)

type literalAssignment struct {
	value       string
	availableAt int
}

// ExpandLiteralAssignments substitutes variables assigned one safe literal
// value in command. Reassigned, dynamic, and single-quoted references remain
// unresolved for the structural parser to handle conservatively.
func ExpandLiteralAssignments(command string) string {
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
		builder.WriteString(substituteLiteralAssignmentRefs(command[segmentStart:index], segmentStart, values))
		nextIndex, ok := skipLiteralAssignmentQuote(command, index)
		if !ok {
			builder.WriteString(command[index:])
			return builder.String()
		}
		builder.WriteString(command[index:nextIndex])
		index = nextIndex
		segmentStart = index
	}
	builder.WriteString(substituteLiteralAssignmentRefs(command[segmentStart:], segmentStart, values))
	return builder.String()
}

func substituteLiteralAssignmentRefs(
	segment string,
	segmentStart int,
	values map[string]literalAssignment,
) string {
	matches := literalAssignmentRefPattern.FindAllStringSubmatchIndex(segment, -1)
	if len(matches) == 0 {
		return segment
	}
	var builder strings.Builder
	previousEnd := 0
	for _, match := range matches {
		matchStart, matchEnd := match[0], match[1]
		nameStart, nameEnd := match[2], match[3]
		if nameStart == -1 {
			nameStart, nameEnd = match[4], match[5]
		}
		assignment, found := values[segment[nameStart:nameEnd]]
		if !found || segmentStart+matchStart < assignment.availableAt || escapedLiteralAssignmentRef(segment, matchStart) {
			continue
		}
		builder.WriteString(segment[previousEnd:matchStart])
		builder.WriteString(assignment.value)
		previousEnd = matchEnd
	}
	if previousEnd == 0 {
		return segment
	}
	builder.WriteString(segment[previousEnd:])
	return builder.String()
}

func escapedLiteralAssignmentRef(segment string, matchStart int) bool {
	backslashCount := 0
	for index := matchStart - 1; 0 <= index && segment[index] == '\\'; index-- {
		backslashCount++
	}
	return backslashCount%2 == 1
}

func literalAssignmentValues(command string) map[string]literalAssignment {
	values := make(map[string]literalAssignment)
	counts := make(map[string]int)
	index := 0
	for index < len(command) {
		commandValues := make(map[string]string)
		commandCounts := make(map[string]int)
		for {
			index = skipLiteralAssignmentSpace(command, index)
			nextIndex, found := readLiteralAssignment(command, index, commandValues, commandCounts)
			if !found {
				break
			}
			index = nextIndex
		}
		assignmentOnly := literalAssignmentCommandEndsAt(command, skipLiteralAssignmentSpace(command, index))
		nextIndex, found := nextLiteralAssignmentCommandStart(command, index)
		if assignmentOnly {
			availableAt := len(command)
			if found {
				availableAt = nextIndex
			}
			for name, count := range commandCounts {
				counts[name] += count
				if value, found := commandValues[name]; found {
					values[name] = literalAssignment{value: value, availableAt: availableAt}
				}
			}
		}
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

func literalAssignmentCommandEndsAt(command string, index int) bool {
	if len(command) <= index || command[index] == ';' || command[index] == '\n' {
		return true
	}
	return index+1 < len(command) && (command[index] == '&' || command[index] == '|') && command[index+1] == command[index]
}

func readLiteralAssignment(command string, index int, values map[string]string, counts map[string]int) (int, bool) {
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
		for valueEnd := index + 1; valueEnd < len(command); valueEnd++ {
			if command[valueEnd] != quote {
				continue
			}
			nextIndex := valueEnd + 1
			if nextIndex < len(command) && !literalAssignmentWordBoundary(command, nextIndex) {
				return "", index, false
			}
			return command[index:nextIndex], nextIndex, true
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
	return !strings.ContainsAny(stripOuterQuotes(value), "$`) \t\n*?[{\\}")
}

func stripOuterQuotes(value string) string {
	if 2 <= len(value) && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
		(value[0] == '"' && value[len(value)-1] == '"')) {
		return value[1 : len(value)-1]
	}
	return value
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
		case '&', '|':
			if index+1 < len(command) && command[index+1] == command[index] {
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
	if strings.ContainsRune(" \t\n;", rune(command[index])) {
		return true
	}
	return index+1 < len(command) && (command[index] == '&' || command[index] == '|') && command[index+1] == command[index]
}

func literalAssignmentNameStart(char byte) bool {
	return char == '_' || betweenLiteralAssignmentChars(char, 'A', 'Z') ||
		betweenLiteralAssignmentChars(char, 'a', 'z')
}

func literalAssignmentNamePart(char byte) bool {
	return literalAssignmentNameStart(char) || betweenLiteralAssignmentChars(char, '0', '9')
}

func betweenLiteralAssignmentChars(char, first, last byte) bool {
	return first <= char && char <= last
}
