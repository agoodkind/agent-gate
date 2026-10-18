package rules

import "strings"

// redirects includes loop-level input redirects that do not belong to a
// simple command. It skips quoted text, comments, and heredoc operators.
func (collector *credentialReadCollector) redirects(source, cwd string, local bool) {
	var quote byte
	escaped := false
	comment := false
	for index := 0; index < len(source); index++ {
		character := source[index]
		if comment {
			comment = character != '\n'
			continue
		}
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		if character == '\'' || character == '"' {
			quote = character
			continue
		}
		if character == '#' && isShellCommentStart(source, index) {
			comment = true
			continue
		}
		if character != '<' {
			continue
		}
		if index+1 < len(source) && strings.ContainsRune("<&(", rune(source[index+1])) {
			index++
			continue
		}
		path, end := readShellWord(source, index+1)
		collector.add(strings.TrimRight(path, ")"), cwd, local)
		index = max(index, end-1)
	}
}

func (collector *credentialReadCollector) patch(body, cwd string) {
	var path string
	var source strings.Builder
	flush := func() {
		if credentialScriptPath(path) {
			collector.source(source.String(), path, cwd, credentialSourceDepth)
		}
		source.Reset()
	}
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "*** ") {
			flush()
			for _, directive := range patchFileDirectives {
				if value, ok := strings.CutPrefix(line, directive); ok {
					path = strings.TrimSpace(value)
				}
			}
			continue
		}
		if strings.HasPrefix(line, "+") || strings.HasPrefix(line, " ") {
			source.WriteString(line[1:])
			source.WriteByte('\n')
		}
	}
	flush()
}
