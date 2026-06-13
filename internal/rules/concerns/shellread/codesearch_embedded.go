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
func extractEmbeddedCodeSearchInto(decomposition *shelldecomp.Decomposition, cwd, home string, tools map[string]bool, add func(string), depth int, resolver shelldecomp.FileResolver) {
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
		extractCodeSearchInto(script, innerCwd, home, tools, add, depth-1, resolver)
	}

	for _, region := range decomposition.EmbeddedRegions() {
		if region.Parsed != nil {
			foldRegionReads(region, tools, add)
			continue
		}
		if region.Lang != shelldecomp.LangOpaque {
			continue
		}
		extractCodeSearchInto(region.Text, cwd, home, tools, add, depth-1, resolver)
	}
}

// regionFoldTools maps an analyzed embedded language to the tool names that, when
// declared by a rule, enable folding that region's analyzer-derived reads. A
// language whose analyzer is registered in shelldecomp (python, awk) appears
// here; a region whose language is absent folds nothing, so adding a new
// analyzer to shelldecomp without a matching entry leaves it inert here.
var regionFoldTools = map[shelldecomp.Lang][]string{
	shelldecomp.LangPython: {"python", "python3"},
	shelldecomp.LangAwk:    {"awk", "gawk"},
}

// foldRegionReads folds the read targets an embedded region's analyzer derived
// (shelldecomp produces these inside region.Parsed) into add, when the rule's
// tool set declares a tool that enables this language. It applies the same
// write-guard as the top level (a path the program also writes is an edit
// target, not a content search) and drops any target shelldecomp could not pin
// to a literal absolute path, so an unresolvable shape stays out of scope. A
// region whose language has no fold entry, or whose enabling tools are not
// declared, folds nothing.
func foldRegionReads(region shelldecomp.EmbeddedRegion, tools map[string]bool, add func(string)) {
	if !regionFoldEnabled(region.Lang, tools) {
		return
	}
	written := make(map[string]struct{})
	for _, target := range region.Parsed.WriteTargets() {
		written[target.Path] = struct{}{}
	}
	for _, target := range region.Parsed.ReadTargets() {
		if _, isWrite := written[target.Path]; isWrite {
			continue
		}
		if !target.Resolvable || target.Path == shelldecomp.Unresolvable {
			continue
		}
		add(target.Path)
	}
}

// regionFoldEnabled reports whether a region's language has a fold entry and the
// rule's tool set declares at least one of that language's enabling tools.
func regionFoldEnabled(lang shelldecomp.Lang, tools map[string]bool) bool {
	toolNames, found := regionFoldTools[lang]
	if !found {
		return false
	}
	for _, name := range toolNames {
		if tools[name] {
			return true
		}
	}
	return false
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
