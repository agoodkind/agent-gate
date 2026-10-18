package rules

import (
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

func (collector *credentialReadCollector) rubySource(body, cwd string, depth int) {
	if strings.TrimSpace(body) == "" {
		return
	}
	if depth <= 0 || len(body) > credentialSourceLimit {
		collector.add(uninspectedScriptMarker, "", false)
		return
	}
	lines := make(map[string]bool)
	for line := range strings.SplitSeq(body, "\n") {
		lines[line] = true
	}
	delimiter := "AGENT_GATE_RUBY_SOURCE"
	for lines[delimiter] {
		delimiter += "_"
	}
	// The parser reads a quoted heredoc as Ruby source without shell word
	// concatenation, which the -e operand parser cannot resolve.
	source := "ruby <<'" + delimiter + "'\n" + body + "\n" + delimiter + "\n"
	parsed := shelldecomp.ParseWithOptions(source, cwd, shelldecomp.Options{Home: cwd, FileResolver: nil})
	collector.decomposition(parsed, cwd, false, depth)
}
