package rules

import (
	"fmt"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

// unparseableCommandMarker is returned by renderCommandAST when gksyntax
// cannot parse the command. The caller always also shows the verbatim
// command text, so this marker stays short rather than repeating it.
const unparseableCommandMarker = "unparseable command; judge from the verbatim text"

// renderMaxEmbeddedDepth bounds the renderer's own recursion into nested
// embedded regions (a -c body whose -c body itself launders another -c
// body, and so on). shelldecomp already caps its own parse depth, but this
// is an independent backstop so a pathological nest cannot make prompt
// rendering unbounded.
const renderMaxEmbeddedDepth = 5

// renderSnippetMaxChars caps the raw-text snippet shown for an embedded
// region. The snippet exists so a region gksyntax could not analyze
// structurally (no grammar, or no matching read/write pattern) still shows
// the judge what code ran there; capping it keeps a large heredoc body
// structural rather than dumped verbatim into the token-metered prompt.
const renderSnippetMaxChars = 160

// renderCommandAST parses command with gksyntax and renders a compact,
// token-frugal structural summary for an LLM judge prompt. It never panics.
func renderCommandAST(command, cwd, home string) string {
	decomposition := shelldecomp.Parse(command, cwd, home)
	if decomposition.IsOpaque() {
		return unparseableCommandMarker
	}

	var builder strings.Builder
	renderDecomposition(&builder, decomposition, cwd, 0)
	return strings.TrimRight(builder.String(), "\n")
}

// renderDecomposition writes the commands, read targets, write targets, and
// embedded regions of one decomposition into builder. topCwd is the
// top-level command's starting directory, threaded through unchanged at
// every recursion depth so a nested command's cwd is only called out when it
// differs from where the judge already believes the command started.
func renderDecomposition(builder *strings.Builder, decomposition *shelldecomp.Decomposition, topCwd string, depth int) {
	renderCommands(builder, decomposition.Commands(), topCwd)
	renderReadTargets(builder, decomposition.ReadTargets())
	renderWriteTargets(builder, decomposition.WriteTargets())
	renderEmbeddedRegions(builder, decomposition.EmbeddedRegions(), topCwd, depth)
}

// renderCommands writes one line per parsed command: its argv0, its
// resolved (or marked-unresolved) arguments, and its cwd when that differs
// from topCwd. An empty command list writes nothing, so a decomposition that
// carries only, say, an assignment produces no empty "cmd:" header.
func renderCommands(builder *strings.Builder, commands []shelldecomp.Command, topCwd string) {
	if len(commands) == 0 {
		return
	}
	builder.WriteString("cmd:\n")
	for _, command := range commands {
		builder.WriteString("  ")
		builder.WriteString(renderCommandLine(command, topCwd))
		builder.WriteString("\n")
	}
}

// renderCommandLine renders one command's argv0 and arguments as a single
// space-joined line, appending a "[cwd: ...]" suffix only when the command
// ran in a directory other than topCwd (for example after a `cd` earlier in
// the same script).
func renderCommandLine(command shelldecomp.Command, topCwd string) string {
	parts := make([]string, 0, len(command.Args)+1)
	parts = append(parts, command.Argv0)
	for _, arg := range command.Args {
		parts = append(parts, renderWord(arg))
	}
	line := strings.Join(parts, " ")
	if command.Cwd != "" && command.Cwd != topCwd {
		line += " [cwd: " + renderResolvablePath(command.Cwd) + "]"
	}
	return line
}

// renderWord renders one argument: its resolved value when the word is
// pinned to a literal, or its raw text with a short unresolved marker
// otherwise (an expansion, a command substitution, or other ambient state
// the parser cannot pin statically).
func renderWord(word shelldecomp.Word) string {
	if word.Resolvable {
		return word.Value
	}
	return word.Text + "<unresolved>"
}

// renderResolvablePath collapses the gksyntax Unresolvable sentinel to a
// readable marker, so the sentinel's raw "\x00..." bytes never reach the
// prompt.
func renderResolvablePath(path string) string {
	if path == shelldecomp.Unresolvable {
		return "<unresolved>"
	}
	return path
}

// renderReadTargets writes one line per read target: the path a command
// reads, which argv0 produced it, and an unresolved marker in place of a
// fabricated path when gksyntax could not pin it to a literal. This is how
// the judge sees what a scan or read touches even when the path arrives
// through a redirect, a heredoc-fed interpreter, or a wrapped command.
func renderReadTargets(builder *strings.Builder, targets []shelldecomp.ReadTarget) {
	if len(targets) == 0 {
		return
	}
	builder.WriteString("reads:\n")
	for _, target := range targets {
		builder.WriteString("  ")
		builder.WriteString(renderTarget(target.Path, target.Resolvable, target.Argv0, target.Raw))
		builder.WriteString("\n")
	}
}

// renderWriteTargets writes one line per write target: the path a command
// writes and which argv0 produced it (a redirect, tee, cp/mv, sed -i, an
// editor, an in-place interpreter edit). This is how the judge sees a write
// that never spells "write" in the verbatim command text.
func renderWriteTargets(builder *strings.Builder, targets []shelldecomp.WriteTarget) {
	if len(targets) == 0 {
		return
	}
	builder.WriteString("writes:\n")
	for _, target := range targets {
		builder.WriteString("  ")
		builder.WriteString(renderTarget(target.Path, target.Resolvable, target.Argv0, target.Raw))
		builder.WriteString("\n")
	}
}

// renderTarget renders one read or write target as "path (argv0)", or an
// unresolved marker naming the raw token in place of a fabricated path when
// gksyntax could not pin the path to a literal.
func renderTarget(path string, resolvable bool, argv0, raw string) string {
	if !resolvable || path == shelldecomp.Unresolvable {
		return fmt.Sprintf("<unresolved: %s> (%s)", raw, argv0)
	}
	return fmt.Sprintf("%s (%s)", path, argv0)
}

// renderEmbeddedRegions writes one block per embedded code region (a
// heredoc body, a -c script, a remote shell, a mini-language program): its
// language, a bounded raw-text snippet, and, when gksyntax parsed the
// region's body, a recursive structural render of that body's own commands,
// read targets, write targets, and further embedded regions. This is where a
// laundered search or write hiding inside a nested shell or interpreter
// becomes visible to the judge. depth guards the recursion independently of
// shelldecomp's own parse-depth cap.
func renderEmbeddedRegions(builder *strings.Builder, regions []shelldecomp.EmbeddedRegion, topCwd string, depth int) {
	if len(regions) == 0 || depth >= renderMaxEmbeddedDepth {
		return
	}
	for _, region := range regions {
		builder.WriteString("embedded[" + region.Lang.String() + "]: " + renderSnippet(region.Text) + "\n")
		if region.Parsed == nil {
			continue
		}
		var nested strings.Builder
		renderDecomposition(&nested, region.Parsed, topCwd, depth+1)
		builder.WriteString(indentLines(nested.String()))
	}
}

// renderSnippet collapses an embedded region's body to single-line
// whitespace and truncates it to renderSnippetMaxChars, so a large heredoc
// or script body stays structural rather than being dumped verbatim into
// the prompt.
func renderSnippet(text string) string {
	collapsed := strings.Join(strings.Fields(text), " ")
	runes := []rune(collapsed)
	if len(runes) <= renderSnippetMaxChars {
		return collapsed
	}
	return string(runes[:renderSnippetMaxChars]) + "…"
}

// indentLines prefixes every non-empty line of text with two spaces, used to
// nest a recursively rendered embedded region's structure under its
// "embedded[...]" header.
func indentLines(text string) string {
	trimmed := strings.TrimRight(text, "\n")
	if trimmed == "" {
		return ""
	}
	lines := strings.Split(trimmed, "\n")
	for index, line := range lines {
		lines[index] = "  " + line
	}
	return strings.Join(lines, "\n") + "\n"
}
