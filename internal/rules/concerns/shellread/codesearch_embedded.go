package shellread

import (
	"regexp"

	"goodkind.io/gksyntax/shelldecomp"
)

// maxEmbeddedSearchDepth bounds the recursion into embedded code (a wrapper
// shell's -c script containing a heredoc containing a script, and so on). Real
// agent commands nest one or two levels; the bound only stops pathological
// inputs.
const maxEmbeddedSearchDepth = 3

// localWrapperShells are the argv0 values whose -c operand runs locally with
// the caller's filesystem. A remote or containerized wrapper (ssh, docker) also
// embeds a shell script, but its paths belong to another machine, so it is
// deliberately absent: shelldecomp's parsed regions carry no origin marker, and
// re-deriving the script from the local wrapper's own argv is what keeps a
// remote search from being mistaken for a local one.
var localWrapperShells = map[string]bool{
	"bash": true,
	"sh":   true,
	"zsh":  true,
	"dash": true,
	"ksh":  true,
}

// dashCFlagRe matches a short-flag cluster that includes -c (for example -c,
// -lc, -ec), whose following operand is the script the wrapper executes.
var dashCFlagRe = regexp.MustCompile(`^-[a-zA-Z]*c$`)

// extractEmbeddedCodeSearchInto recurses into embedded code the command would
// execute on this machine: the -c script of a local wrapper shell, taken from
// the wrapper's own argv so remote wrappers stay excluded, and an opaque
// heredoc body, which covers the temp-script shape (`cat > "$S" <<'EOF' ...
// EOF; bash "$S"`). A heredoc body is parsed as shell best-effort: prose
// yields no declared-tool commands and therefore no targets, and a relative
// path inside the body resolves against the outer cwd because the body's
// execution-time cwd is unknowable statically.
func extractEmbeddedCodeSearchInto(decomposition *shelldecomp.Decomposition, cwd, home string, tools map[string]bool, add func(string), depth int) {
	for _, cmd := range decomposition.Commands() {
		if !localWrapperShells[cmd.Argv0] {
			continue
		}
		script, ok := dashCOperand(cmd)
		if !ok {
			continue
		}
		innerCwd := cmd.Cwd
		if innerCwd == "" || innerCwd == shelldecomp.Unresolvable {
			innerCwd = cwd
		}
		extractCodeSearchInto(script, innerCwd, home, tools, add, depth-1)
	}

	for _, region := range decomposition.EmbeddedRegions() {
		if region.Lang != shelldecomp.LangOpaque || region.Parsed != nil {
			continue
		}
		extractCodeSearchInto(region.Text, cwd, home, tools, add, depth-1)
	}
}

// dashCOperand returns the script operand of a wrapper shell invoked with -c
// (alone or inside a short-flag cluster like -lc), and whether one was found.
// An unresolvable script operand (a $var) yields nothing rather than a guess.
func dashCOperand(cmd shelldecomp.Command) (string, bool) {
	for index, arg := range cmd.Args {
		if !dashCFlagRe.MatchString(arg.Text) {
			continue
		}
		if index+1 >= len(cmd.Args) {
			return "", false
		}
		script := cmd.Args[index+1]
		if !script.Resolvable {
			return "", false
		}
		return script.Value, true
	}
	return "", false
}
