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
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellargs"
	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
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
// A command whose body is opaque to the top-level scan (eval/exec, an
// interpreter run with -c, or a command substitution) yields a sentinel so a
// glob rule can default-deny. shelldecomp still parses those embedded bodies, so
// this function also recurses into them and returns any resolved embedded write
// target (for example the f in `bash -c 'echo x > f'`, cd-aware); the sentinel is
// emitted alongside so both the default-deny and the concrete-target consumers
// are served. A process-substitution redirect (`>(cmd)`) is not a file write and
// is dropped.
func ExtractWriteTargets(cmd, cwd string) []WriteTarget {
	return ExtractWriteTargetsWithSpecs(cmd, cwd, nil)
}

// ExtractWriteTargetsWithSpecs returns the built-in structural write targets
// plus targets declared through generic command-shape specifications.
func ExtractWriteTargetsWithSpecs(cmd, cwd string, specs []config.ShellWriteSpec) []WriteTarget {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}
	cmd = shellparse.ExpandLiteralAssignments(cmd)
	home := homeDir()
	decomposition := shelldecomp.Parse(cmd, cwd, home)
	if decomposition.IsOpaque() {
		return []WriteTarget{{Path: "", Tool: ToolUnparseable, Reason: ReasonUnparsedCommandShape, Raw: cmd}}
	}

	var out []WriteTarget
	sentinel, opaque := unparseableSentinel(decomposition)
	if opaque {
		out = append(out, sentinel)
	}
	for _, target := range decomposition.WriteTargets() {
		if replacedArgumentTarget(decomposition, target, specs) {
			continue
		}
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
	out = append(out, declaredWriteTargets(decomposition, specs)...)
	out = append(out, commonWriteTargets(decomposition, specs)...)
	// Only recurse into embedded bodies for the local opaque shapes the sentinel
	// already covers (eval/exec, interpreter -c, command substitution). This
	// surfaces a real local write hidden inside `bash -c 'echo x > f'` (f,
	// cd-aware) while deliberately not descending into a remote shell (ssh),
	// whose writes are remote, not local files, and which never triggers the
	// sentinel. An unresolved embedded write is still covered by the sentinel.
	if opaque {
		out = append(out, embeddedWriteTargets(decomposition, specs, 0)...)
	}
	return dedupeWriteTargets(out)
}

func declaredWriteTargets(decomposition *shelldecomp.Decomposition, specs []config.ShellWriteSpec) []WriteTarget {
	if len(specs) == 0 {
		return nil
	}
	var targets []WriteTarget
	for _, command := range decomposition.Commands() {
		for _, spec := range specs {
			if !slices.Contains(spec.Argv0, filepath.Base(command.Argv0)) {
				continue
			}
			targets = append(targets, declaredCommandTargets(command, spec)...)
		}
	}
	return targets
}

func declaredCommandTargets(command shelldecomp.Command, spec config.ShellWriteSpec) []WriteTarget {
	result := shellargs.Parse(command, spec.ArgumentDescription())
	if !result.Selected {
		return nil
	}
	operands := result.Operands
	if spec.TargetMode == config.WriteTargetLastOperand && len(operands) > 1 {
		operands = operands[len(operands)-1:]
	}
	if !result.ActivationUnresolved {
		for _, name := range spec.TargetOptions {
			operands = append(operands, result.Options[name]...)
		}
	}
	targets := make([]WriteTarget, 0, len(operands))
	if result.Unresolved && spec.Unresolved != "ignore" {
		targets = append(targets, sentinelFor(command.Argv0))
	}
	for _, operand := range operands {
		if !operand.Resolvable {
			if spec.Unresolved != "ignore" {
				targets = append(targets, sentinelFor(command.Argv0))
			}
			continue
		}
		path := resolveDeclaredPath(operand.Value, result.CWD)
		if path == "" {
			if spec.Unresolved != "ignore" {
				targets = append(targets, sentinelFor(command.Argv0))
			}
			continue
		}
		targets = append(targets, WriteTarget{Path: path, Tool: command.Argv0, Reason: ReasonOK, Raw: operand.Text})
	}
	return targets
}

func resolveDeclaredPath(value string, cwd string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if cwd == "" || cwd == shelldecomp.Unresolvable {
		return ""
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func dedupeWriteTargets(targets []WriteTarget) []WriteTarget {
	seen := make(map[string]struct{}, len(targets))
	out := make([]WriteTarget, 0, len(targets))
	for _, target := range targets {
		key := target.Path
		if key == "" {
			key = target.Reason + "\x00" + target.Tool + "\x00" + target.Raw
		}
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, target)
	}
	return out
}

// maxEmbeddedDepth bounds recursion into nested embedded regions. shelldecomp
// already caps its own parse depth, so this is a defensive backstop.
const maxEmbeddedDepth = 5

// embeddedWriteTargets collects the resolved write targets of every parsed
// embedded region (an interpreter -c body, a command substitution, a heredoc fed
// to an interpreter), recursively. It surfaces both structural writes and
// editor or in-place interpreter edits (for example `bash -c "vim config.go"`),
// so wrapping an edit in a subshell does not evade the checkout guard.
// Unresolvable embedded writes are dropped; the outer sentinel already covers the
// default-deny case for those.
func embeddedWriteTargets(decomposition *shelldecomp.Decomposition, specs []config.ShellWriteSpec, depth int) []WriteTarget {
	if decomposition == nil || depth >= maxEmbeddedDepth {
		return nil
	}
	var out []WriteTarget
	for _, region := range decomposition.EmbeddedRegions() {
		if region.Parsed == nil {
			continue
		}
		for _, target := range region.Parsed.WriteTargets() {
			if replacedArgumentTarget(region.Parsed, target, specs) {
				continue
			}
			if !target.Resolvable || target.Path == shelldecomp.Unresolvable {
				continue
			}
			out = append(out, WriteTarget{
				Path:   target.Path,
				Tool:   toolLabel(target.Argv0),
				Reason: ReasonOK,
				Raw:    target.Raw,
			})
		}
		out = append(out, declaredWriteTargets(region.Parsed, specs)...)
		out = append(out, commonWriteTargets(region.Parsed, specs)...)
		out = append(out, embeddedWriteTargets(region.Parsed, specs, depth+1)...)
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

// commonWriteTargets applies shipped descriptions unless a condition replaces them.
func commonWriteTargets(decomposition *shelldecomp.Decomposition, specs []config.ShellWriteSpec) []WriteTarget {
	commonSpecs, err := config.CommonWriteSpecs()
	if err != nil {
		return []WriteTarget{sentinelFor(err.Error())}
	}
	var targets []WriteTarget
	for _, command := range decomposition.Commands() {
		overridden := false
		for _, spec := range specs {
			if !slices.Contains(spec.Argv0, filepath.Base(command.Argv0)) {
				continue
			}
			if shellargs.Parse(command, spec.ArgumentDescription()).Matched {
				overridden = true
				break
			}
		}
		if overridden {
			continue
		}
		for _, spec := range commonSpecs {
			if slices.Contains(spec.Argv0, filepath.Base(command.Argv0)) {
				targets = append(targets, declaredCommandTargets(command, spec)...)
			}
		}
	}
	return targets
}

func replacedArgumentTarget(decomposition *shelldecomp.Decomposition, target shelldecomp.WriteTarget, specs []config.ShellWriteSpec) bool {
	if target.Origin != shelldecomp.WriteOriginArgument {
		return false
	}
	for _, command := range decomposition.Commands() {
		if command.Node == nil || command.Node.StartByte != target.CommandStartByte {
			continue
		}
		for _, spec := range specs {
			if !slices.Contains(spec.Argv0, filepath.Base(command.Argv0)) {
				continue
			}
			if shellargs.Parse(command, spec.ArgumentDescription()).Matched {
				return true
			}
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
