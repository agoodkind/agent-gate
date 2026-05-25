package rules

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"goodkind.io/agent-gate/internal/config"
	concernlimit "goodkind.io/agent-gate/internal/rules/concerns/limit"
)

const maxDiagnosticMatches = concernlimit.MaxCollectedMatchesPerEvaluation

type diagnosticOccurrence struct {
	Violation
	Key      string
	Line     int
	Column   int
	LineText string
	Match    string
}

// FormatViolations renders concrete matches as line-numbered diagnostics with
// compact rule legend labels.
func FormatViolations(violations []Violation) string {
	if len(violations) == 0 {
		return ""
	}

	omitted := 0
	if len(violations) > maxDiagnosticMatches {
		omitted = len(violations) - maxDiagnosticMatches
		violations = violations[:maxDiagnosticMatches]
	}

	messageOnly, detailed := splitDiagnosticFormats(violations)

	var b strings.Builder
	writeMessageOnlyDiagnostics(&b, messageOnly)
	if len(detailed) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		writeDetailedDiagnostics(&b, detailed)
	}
	if omitted > 0 && len(detailed) > 0 {
		fmt.Fprintf(&b, "\n\n... %d more %s omitted\n", omitted, plural(omitted, "violation", "violations"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func splitDiagnosticFormats(violations []Violation) ([]Violation, []Violation) {
	var messageOnly []Violation
	var detailed []Violation
	for _, violation := range violations {
		if violation.DiagnosticFormat == config.DiagnosticFormatMessageOnly {
			messageOnly = append(messageOnly, violation)
			continue
		}
		detailed = append(detailed, violation)
	}
	return messageOnly, detailed
}

func writeMessageOnlyDiagnostics(b *strings.Builder, violations []Violation) {
	seen := make(map[string]bool)
	for _, violation := range violations {
		if violation.Message == "" || seen[violation.Message] {
			continue
		}
		seen[violation.Message] = true
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(violation.Message)
	}
}

func writeDetailedDiagnostics(b *strings.Builder, violations []Violation) {
	keys := legendKeys(violations)
	occurrences := make([]diagnosticOccurrence, 0, len(violations))
	for _, v := range violations {
		occ := occurrenceFor(v)
		occ.Key = keys[legendID(v)]
		occurrences = append(occurrences, occ)
	}

	fmt.Fprintf(b, "agent-gate blocked %d %s:\n", len(violations), plural(len(violations), "violation", "violations"))
	writeLegend(b, occurrences)
	writeSourceDiagnostics(b, occurrences)
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
	if occ.Redact {
		fmt.Fprintf(b, "    match: %s\n", strconv.Quote("<redacted>"))
		fmt.Fprintf(b, "    text: %s\n", strconv.Quote("<redacted>"))
		return
	}
	fmt.Fprintf(b, "    match: %s\n", strconv.Quote(occ.Match))
	fmt.Fprintf(b, "    text: %s\n", strconv.Quote(occ.LineText))
}

func writeLegend(b *strings.Builder, occurrences []diagnosticOccurrence) {
	seen := make(map[string]bool)
	var order []diagnosticOccurrence
	for _, occ := range occurrences {
		id := legendID(occ.Violation)
		if seen[id] {
			continue
		}
		seen[id] = true
		order = append(order, occ)
	}

	if len(order) == 0 {
		return
	}

	fmt.Fprintf(b, "\nRules:\n")
	for _, occ := range order {
		fmt.Fprintf(b, "- %s = %s\n", occ.Key, occ.RuleName)
		fmt.Fprintf(b, "  message: %s\n", occ.Message)
	}
}

func occurrenceFor(v Violation) diagnosticOccurrence {
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
		Violation: v,
		Line:      line,
		Column:    utf8.RuneCountInString(prefix) + 1,
		LineText:  clippedLineText(visibleText(lineText), utf8.RuneCountInString(prefix), visibleWidth(match)),
		Match:     visibleText(match),
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

func legendKeys(violations []Violation) map[string]string {
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

func legendID(v Violation) string {
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
