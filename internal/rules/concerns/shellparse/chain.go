package shellparse

import "strings"

// SplitCommandChain splits a shell command at unquoted sequence operators.
// A single pipeline operator remains inside a segment because cmd_segments
// treats a pipeline as one submitted operation.
func SplitCommandChain(command string) []string {
	var segments []string
	var current strings.Builder
	var quote rune
	escaped := false
	runes := []rune(command)

	flush := func() {
		segment := strings.TrimSpace(current.String())
		if segment != "" {
			segments = append(segments, segment)
		}
		current.Reset()
	}

	for index := 0; index < len(runes); index++ {
		character := runes[index]
		if escaped {
			current.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			current.WriteRune(character)
			escaped = true
			continue
		}
		if quote != 0 {
			current.WriteRune(character)
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			current.WriteRune(character)
			quote = character
			continue
		}
		if index+1 < len(runes) && isLogicalSeparator(character, runes[index+1]) {
			flush()
			index++
			continue
		}
		if character == ';' || character == '\n' {
			flush()
			continue
		}
		current.WriteRune(character)
	}
	flush()
	return segments
}

func isLogicalSeparator(character rune, next rune) bool {
	return character == '&' && next == '&' || character == '|' && next == '|'
}
