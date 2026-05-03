package rules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const maxDiagnosticMatches = 50

type diagnosticOccurrence struct {
	MatchViolation
	Key      string
	Line     int
	Column   int
	LineText string
	Match    string
}

// FormatViolations renders concrete matches as line-numbered diagnostics with
// compact rule legend labels.
func FormatViolations(violations []MatchViolation) string {
	if len(violations) == 0 {
		return ""
	}

	omitted := 0
	if len(violations) > maxDiagnosticMatches {
		omitted = len(violations) - maxDiagnosticMatches
		violations = violations[:maxDiagnosticMatches]
	}

	keys := legendKeys(violations)
	occurrences := make([]diagnosticOccurrence, 0, len(violations))
	for _, v := range violations {
		occ := occurrenceFor(v)
		occ.Key = keys[legendID(v)]
		occurrences = append(occurrences, occ)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "agent-gate blocked %d %s:\n", len(violations)+omitted, plural(len(violations)+omitted, "violation", "violations"))
	writeSourceDiagnostics(&b, occurrences)
	if omitted > 0 {
		fmt.Fprintf(&b, "\n... %d more %s omitted\n", omitted, plural(omitted, "violation", "violations"))
	}
	writeLegend(&b, occurrences)
	return strings.TrimRight(b.String(), "\n")
}

func writeSourceDiagnostics(b *strings.Builder, occurrences []diagnosticOccurrence) {
	byField := make(map[string][]diagnosticOccurrence)
	var fields []string
	for _, occ := range occurrences {
		if _, ok := byField[occ.FieldPath]; !ok {
			fields = append(fields, occ.FieldPath)
		}
		byField[occ.FieldPath] = append(byField[occ.FieldPath], occ)
	}

	if len(fields) == 0 {
		return
	}

	fmt.Fprintf(b, "\nMatches:\n")
	for _, field := range fields {
		fmt.Fprintf(b, "- field: %s\n", field)
		lines := byField[field]
		sort.SliceStable(lines, func(i, j int) bool {
			if lines[i].Line != lines[j].Line {
				return lines[i].Line < lines[j].Line
			}
			return lines[i].Column < lines[j].Column
		})

		for _, occ := range lines {
			writeOccurrence(b, occ)
		}
	}
}

func writeOccurrence(b *strings.Builder, occ diagnosticOccurrence) {
	fmt.Fprintf(b, "  - rule: %s\n", occ.Key)
	if occ.FilePath != "" {
		fmt.Fprintf(b, "    file: %s\n", occ.FilePath)
	}
	fmt.Fprintf(b, "    line: %d\n", occ.Line)
	fmt.Fprintf(b, "    column: %d\n", occ.Column)
	fmt.Fprintf(b, "    match: %s\n", strconv.QuoteToASCII(occ.Match))
	fmt.Fprintf(b, "    text: %s\n", strconv.QuoteToASCII(occ.LineText))
}

func writeLegend(b *strings.Builder, occurrences []diagnosticOccurrence) {
	seen := make(map[string]bool)
	var order []diagnosticOccurrence
	for _, occ := range occurrences {
		id := legendID(occ.MatchViolation)
		if seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, occ)
	}

	for _, occ := range order {
		fmt.Fprintf(b, "\n%s = %s\n", occ.Key, occ.RuleName)
		fmt.Fprintf(b, "    message: %s\n", occ.Message)
		fmt.Fprintf(b, "    occurrences:\n")
		for _, item := range occurrences {
			if item.Key != occ.Key {
				continue
			}
			fmt.Fprintf(b, "      - field: %s\n", item.FieldPath)
			if item.FilePath != "" {
				fmt.Fprintf(b, "        file: %s\n", item.FilePath)
			}
			fmt.Fprintf(b, "        line: %d\n", item.Line)
			fmt.Fprintf(b, "        column: %d\n", item.Column)
		}
	}
}

func occurrenceFor(v MatchViolation) diagnosticOccurrence {
	line, lineStart, lineText := lineForOffset(v.Value, v.Start)
	startInLine := v.Start - lineStart
	endInLine := v.End - lineStart
	if endInLine < startInLine {
		endInLine = startInLine
	}
	if endInLine > len(lineText) {
		endInLine = len(lineText)
	}

	prefix := lineText[:startInLine]
	match := lineText[startInLine:endInLine]

	return diagnosticOccurrence{
		MatchViolation: v,
		Line:           line,
		Column:         utf8.RuneCountInString(prefix) + 1,
		LineText:       clippedLineText(visibleText(lineText), utf8.RuneCountInString(prefix), visibleWidth(match)),
		Match:          visibleText(match),
	}
}

func lineForOffset(value string, offset int) (line int, lineStart int, text string) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(value) {
		offset = len(value)
	}

	line = 1
	lineStart = 0
	for i, r := range value {
		if i >= offset {
			break
		}
		if r == '\n' {
			line++
			lineStart = i + 1
		}
	}

	lineEnd := len(value)
	if next := strings.IndexByte(value[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	return line, lineStart, value[lineStart:lineEnd]
}

func clippedLineText(line string, matchStart int, matchWidth int) string {
	const maxWidth = 120
	lineRunes := []rune(line)
	if len(lineRunes) <= maxWidth {
		return line
	}

	if matchWidth < 1 {
		matchWidth = 1
	}
	start := matchStart - 30
	if start < 0 {
		start = 0
	}
	end := matchStart + matchWidth + 30
	if end > len(lineRunes) {
		end = len(lineRunes)
	}
	prefix := ""
	if start > 0 {
		prefix = "..."
	}
	suffix := ""
	if end < len(lineRunes) {
		suffix = "..."
	}
	return prefix + string(lineRunes[start:end]) + suffix
}

func legendKeys(violations []MatchViolation) map[string]string {
	keys := make(map[string]string)
	next := 0
	for _, v := range violations {
		id := legendID(v)
		if _, ok := keys[id]; ok {
			continue
		}
		keys[id] = legendKey(next)
		next++
	}
	return keys
}

func legendID(v MatchViolation) string {
	return v.RuleName + "\x00" + v.Message
}

func legendKey(i int) string {
	i++
	var out []byte
	for i > 0 {
		i--
		out = append([]byte{byte('A' + i%26)}, out...)
		i /= 26
	}
	return string(out)
}

func visibleText(s string) string {
	s = strings.ReplaceAll(s, "\t", "<TAB>")
	s = strings.ReplaceAll(s, "\r", "<CR>")
	return s
}

func visibleWidth(s string) int {
	return utf8.RuneCountInString(visibleText(s))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
