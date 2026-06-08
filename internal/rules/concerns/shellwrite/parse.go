// Package shellwrite parses shell command strings and extracts the file paths
// they will write to. The output is a list of [WriteTarget] values which a
// rule can match against a glob list. The parse is delegated to
// goodkind.io/gksyntax/shelldecomp, which decomposes the command structurally
// (tree-sitter) rather than by regex. Write shapes shelldecomp cannot pin to a
// literal path (a redirect to an expansion, a dd of=$VAR, an indirect target)
// arrive with Resolvable false and are surfaced as a sentinel target with the
// [ReasonUnparsedCommandShape] reason so a rule can choose to default-deny.
package shellwrite

import (
	"os"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

// Tool labels classify the kind of write detected. They are exported so a
// rule can include the label in a violation message if useful.
const (
	ToolRedirect    = "redirect"
	ToolTee         = "tee"
	ToolSed         = "sed"
	ToolAwk         = "awk"
	ToolPatch       = "patch"
	ToolGitApply    = "git-apply"
	ToolHeredoc     = "heredoc"
	ToolUnparseable = "unparseable"
)

// Reason classifies why the parser produced this target. Successful targets
// use [ReasonOK]; sentinel targets use [ReasonUnparsedCommandShape].
const (
	ReasonOK                   = "ok"
	ReasonUnparsedCommandShape = "unparsed-command-shape"
)

// WriteTarget describes one file path that a shell command will write to.
type WriteTarget struct {
	// Path is the resolved path of the write target. When relative, it is
	// resolved against cwd. Empty when Reason is [ReasonUnparsedCommandShape].
	Path string
	// Tool labels the shape that produced this target.
	Tool string
	// Reason is [ReasonOK] for resolved targets and
	// [ReasonUnparsedCommandShape] for sentinel targets the parser could
	// not statically resolve.
	Reason string
	// Raw is the original token this target was extracted from.
	Raw string
}

// ExtractWriteTargets parses cmd with shelldecomp and returns one [WriteTarget]
// for each recognized write shape. Relative paths are joined to cwd. shelldecomp
// recognizes output redirections (> >> &>), tee, dd of=, sed -i, awk -i inplace,
// patch, and git apply, resolving each against the cwd in effect at its command
// (so a write after `cd /other` is attributed to /other).
//
// A write whose destination shelldecomp cannot pin to a literal (a redirect to
// $VAR, dd of=$VAR) arrives with Resolvable false; it is surfaced as a single
// [ReasonUnparsedCommandShape] sentinel so the rule can default-deny rather than
// act on a fabricated path.
//
// A command whose body is opaque to static gating (eval/exec, an interpreter
// run with -c, or a command substitution) also yields a sentinel: its real
// writes are hidden inside a shape the gate cannot enumerate, so the
// conservative answer is to default-deny, matching the prior regex parser. A
// process-substitution redirect (`>(cmd)`) is not a file write and is dropped.
func ExtractWriteTargets(cmd, cwd string) []WriteTarget {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	home := homeDir()
	decomposition := shelldecomp.Parse(cmd, cwd, home)
	if decomposition.IsOpaque() {
		return []WriteTarget{{Path: "", Tool: ToolUnparseable, Reason: ReasonUnparsedCommandShape, Raw: cmd}}
	}

	var out []WriteTarget
	if sentinel, ok := unparseableSentinel(decomposition); ok {
		out = append(out, sentinel)
	}
	for _, target := range decomposition.WriteTargets() {
		if !target.Resolvable || target.Path == shelldecomp.Unresolvable {
			out = append(out, WriteTarget{
				Path:   "",
				Tool:   ToolUnparseable,
				Reason: ReasonUnparsedCommandShape,
				Raw:    target.Raw,
			})
			continue
		}
		out = append(out, WriteTarget{
			Path:   target.Path,
			Tool:   toolLabel(target.Argv0),
			Reason: ReasonOK,
			Raw:    target.Raw,
		})
	}
	return out
}

// unparseableSentinel reports whether any command in the decomposition runs a
// shape whose writes the gate cannot statically enumerate, returning one
// [ReasonUnparsedCommandShape] target so the rule can default-deny. It fires for
// eval/exec, an interpreter invoked with -c (whose -c body is opaque to glob
// matching), and any command carrying a command-substitution operand.
func unparseableSentinel(decomposition *shelldecomp.Decomposition) (WriteTarget, bool) {
	for _, command := range decomposition.Commands() {
		if command.Argv0 == "eval" || command.Argv0 == "exec" {
			return sentinelFor(command.Argv0), true
		}
		if isInterpreterWithScript(command) {
			return sentinelFor(command.Argv0), true
		}
		if hasCommandSubstitution(command) {
			return sentinelFor(command.Argv0), true
		}
	}
	return WriteTarget{Path: "", Tool: "", Reason: "", Raw: ""}, false
}

// sentinelFor builds a ReasonUnparsedCommandShape target labeled with the
// command that produced the opaque shape.
func sentinelFor(argv0 string) WriteTarget {
	return WriteTarget{Path: "", Tool: ToolUnparseable, Reason: ReasonUnparsedCommandShape, Raw: argv0}
}

// shellInterpreters are the argv0 names whose -c argument is a quoted program
// the outer gate cannot statically resolve into write targets.
var shellInterpreters = map[string]bool{
	"bash": true, "sh": true, "zsh": true, "ksh": true, "dash": true,
	"python": true, "python3": true, "perl": true, "ruby": true, "node": true,
}

// isInterpreterWithScript reports whether a command runs a known interpreter
// with a -c flag, whose script body is opaque to static write-target analysis.
func isInterpreterWithScript(command shelldecomp.Command) bool {
	if !shellInterpreters[command.Argv0] {
		return false
	}
	for _, arg := range command.Args {
		if arg.Text == "-c" {
			return true
		}
	}
	return false
}

// hasCommandSubstitution reports whether a command carries an operand that is a
// $(...) or `...` command substitution, which the prior parser treated as an
// unparseable shape because a write could hide inside the substituted command.
func hasCommandSubstitution(command shelldecomp.Command) bool {
	for _, arg := range command.Args {
		if arg.Resolvable {
			continue
		}
		if strings.Contains(arg.Text, "$(") || strings.Contains(arg.Text, "`") {
			return true
		}
	}
	return false
}

// toolLabelByArgv0 maps a shelldecomp write argv0 to a Tool label. A writer
// named through a redirect carries its command as argv0 (echo, cat), so an argv0
// absent from this map is labeled a redirect.
var toolLabelByArgv0 = map[string]string{
	"tee":       ToolTee,
	"sed":       ToolSed,
	"awk":       ToolAwk,
	"gawk":      ToolAwk,
	"patch":     ToolPatch,
	"git apply": ToolGitApply,
}

// toolLabel returns the Tool label for a shelldecomp write argv0, defaulting to
// a redirect for any command not in the inline-write set.
func toolLabel(argv0 string) string {
	if label, ok := toolLabelByArgv0[argv0]; ok {
		return label
	}
	return ToolRedirect
}

// homeDir returns the user's home directory for tilde expansion, or "" when it
// cannot be determined. A tilde left unexpanded resolves to an unresolvable
// target rather than a fabricated path.
func homeDir() string {
	dir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return dir
}
