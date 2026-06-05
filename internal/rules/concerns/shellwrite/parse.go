// Package shellwrite parses shell command strings and extracts the file paths
// they will write to. The output is a list of [WriteTarget] values which a
// rule can match against a glob list. Commands the parser cannot resolve
// (eval, command substitution, indirect script invocation) yield a sentinel
// target with the [ReasonUnparsedCommandShape] reason so a rule can choose
// to default-deny those cases.
package shellwrite

import (
	"path/filepath"
	"slices"
	"strings"
	"unicode"
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
	// Raw is the original command segment this target was extracted from.
	Raw string
}

// ExtractWriteTargets parses cmd and returns one [WriteTarget] for each
// recognized write shape. Relative paths are joined to cwd. The parser
// recognizes:
//
//   - Output redirections: > path, >> path, &> path, &>> path
//   - Heredoc into a redirection: cat <<EOF >> path ... EOF
//   - tee path / tee -a path
//   - sed -i path (with optional backup suffix like -i.bak)
//   - awk -i inplace ... path
//   - patch path / patch -p<N> path
//   - git apply path / git apply --index path
//
// Shapes the parser cannot statically resolve (eval, $(...), `...`, indirect
// invocation through a variable) yield a single sentinel target with
// [ReasonUnparsedCommandShape] so the rule can choose to default-deny.
func ExtractWriteTargets(cmd, cwd string) []WriteTarget {
	if strings.TrimSpace(cmd) == "" {
		return nil
	}

	stripped := stripHeredocBodies(cmd)

	var out []WriteTarget
	for _, segment := range splitChain(stripped) {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			continue
		}
		if hasUnparseableShape(segment) {
			out = append(out, WriteTarget{
				Path:   "",
				Tool:   ToolUnparseable,
				Reason: ReasonUnparsedCommandShape,
				Raw:    segment,
			})
			continue
		}
		out = append(out, extractFromSegment(segment, cwd)...)
	}
	return out
}

// extractFromSegment returns the write targets in one chain segment. A single
// segment can produce multiple targets (for example `tee a >> b` writes to
// both files).
func extractFromSegment(segment, cwd string) []WriteTarget {
	fields := shellFields(segment)
	if len(fields) == 0 {
		return nil
	}

	var out []WriteTarget
	out = append(out, redirectionTargets(fields, segment, cwd)...)
	out = append(out, commandWriteTargets(fields, segment, cwd)...)
	return out
}

// redirectionTargets walks the field list and emits a [WriteTarget] for every
// > and >> redirection. The parser treats `&>` and `&>>` as equivalent
// shapes. Process substitution (`>(cmd)`) is intentionally not classified
// as a write target here since the destination is the substituted command,
// not a file.
func redirectionTargets(fields []string, raw, cwd string) []WriteTarget {
	var out []WriteTarget
	for index, field := range fields {
		if !isRedirectToken(field) {
			continue
		}
		if index+1 >= len(fields) {
			continue
		}
		next := fields[index+1]
		if isProcessSubstitution(next) {
			continue
		}
		out = append(out, WriteTarget{
			Path:   resolvePath(cwd, next),
			Tool:   ToolRedirect,
			Reason: ReasonOK,
			Raw:    raw,
		})
	}
	return out
}

var redirectTokens = map[string]struct{}{
	">":   {},
	">>":  {},
	"&>":  {},
	"&>>": {},
}

func isRedirectToken(field string) bool {
	_, present := redirectTokens[field]
	return present
}

// isProcessSubstitution reports whether next is the head of a `>(...)` or
// `<(...)` process-substitution form. We treat such tokens as non-file
// destinations so the redirection is not classified as a write target.
func isProcessSubstitution(next string) bool {
	if strings.HasPrefix(next, "(") {
		return true
	}
	if strings.HasPrefix(next, ">(") || strings.HasPrefix(next, "<(") {
		return true
	}
	return false
}

// commandWriteHandlers dispatches argv0 names to per-tool extractors. Using a
// map rather than a switch keeps the code free of the bare-string-switch
// staticcheck-extra finding and makes it cheap to add new shapes.
var commandWriteHandlers = map[string]func(args []string, raw, cwd string) []WriteTarget{
	"tee":   teeTargets,
	"sed":   sedTargets,
	"awk":   awkTargets,
	"patch": patchTargets,
	"git":   gitTargets,
}

// commandWriteTargets handles tool-shaped writes such as `tee`, `sed -i`,
// `awk -i inplace`, `patch`, and `git apply`.
func commandWriteTargets(fields []string, raw, cwd string) []WriteTarget {
	if len(fields) == 0 {
		return nil
	}
	argv0 := filepath.Base(fields[0])
	handler, ok := commandWriteHandlers[argv0]
	if !ok {
		return nil
	}
	return handler(fields[1:], raw, cwd)
}

// teeTargets returns every non-flag positional argument as a write target.
// `tee` writes to all listed files (with -a for append, no semantic change
// for the purposes of this Condition, both shapes are writes).
func teeTargets(args []string, raw, cwd string) []WriteTarget {
	var out []WriteTarget
	for _, arg := range args {
		if isFlag(arg) {
			continue
		}
		if isRedirectToken(arg) {
			break
		}
		out = append(out, WriteTarget{
			Path:   resolvePath(cwd, arg),
			Tool:   ToolTee,
			Reason: ReasonOK,
			Raw:    raw,
		})
	}
	return out
}

// sedTargets returns the last positional argument as a write target when the
// args list contains -i (in-place edit). The -i flag may carry a backup
// suffix attached (for example -i.bak) which is still treated as in-place.
func sedTargets(args []string, raw, cwd string) []WriteTarget {
	if !hasSedInPlace(args) {
		return nil
	}
	last := lastPositional(args)
	if last == "" {
		return nil
	}
	return []WriteTarget{{
		Path:   resolvePath(cwd, last),
		Tool:   ToolSed,
		Reason: ReasonOK,
		Raw:    raw,
	}}
}

func hasSedInPlace(args []string) bool {
	for _, arg := range args {
		if arg == "-i" || strings.HasPrefix(arg, "-i") && !strings.HasPrefix(arg, "--") {
			return true
		}
	}
	return false
}

// awkTargets returns the last positional when the args list contains both
// `-i` and `inplace`. Awk's inplace mode is a two-token flag plus value.
func awkTargets(args []string, raw, cwd string) []WriteTarget {
	if !hasAwkInPlace(args) {
		return nil
	}
	last := lastPositional(args)
	if last == "" {
		return nil
	}
	return []WriteTarget{{
		Path:   resolvePath(cwd, last),
		Tool:   ToolAwk,
		Reason: ReasonOK,
		Raw:    raw,
	}}
}

func hasAwkInPlace(args []string) bool {
	for index, arg := range args {
		if arg != "-i" {
			continue
		}
		if index+1 < len(args) && args[index+1] == "inplace" {
			return true
		}
	}
	return false
}

// patchTargets returns the path argument for `patch` invocations. The shape
// `patch <path>` and `patch -p<N> <path>` are both supported.
func patchTargets(args []string, raw, cwd string) []WriteTarget {
	for _, arg := range args {
		if isFlag(arg) {
			continue
		}
		return []WriteTarget{{
			Path:   resolvePath(cwd, arg),
			Tool:   ToolPatch,
			Reason: ReasonOK,
			Raw:    raw,
		}}
	}
	return nil
}

// gitTargets handles `git apply [flags] path`. Other git subcommands are not
// treated as direct write targets; they are out of scope for this Condition.
func gitTargets(args []string, raw, cwd string) []WriteTarget {
	if len(args) == 0 || args[0] != "apply" {
		return nil
	}
	for _, arg := range args[1:] {
		if isFlag(arg) {
			continue
		}
		return []WriteTarget{{
			Path:   resolvePath(cwd, arg),
			Tool:   ToolGitApply,
			Reason: ReasonOK,
			Raw:    raw,
		}}
	}
	return nil
}

// lastPositional returns the last argument that does not start with a dash.
// Empty when no such argument exists.
func lastPositional(args []string) string {
	for _, arg := range slices.Backward(args) {
		if isFlag(arg) {
			continue
		}
		return arg
	}
	return ""
}

func isFlag(arg string) bool {
	if len(arg) == 0 {
		return false
	}
	if arg == "-" || arg == "--" {
		return false
	}
	return arg[0] == '-'
}

// alwaysUnparseable lists argv0 names that always count as unparseable.
var alwaysUnparseable = map[string]struct{}{
	"eval": {},
	"exec": {},
}

// shellInterpreters lists argv0 names that are unparseable only when invoked
// with a -c flag (since the inner command is a quoted string).
var shellInterpreters = map[string]struct{}{
	"bash": {},
	"sh":   {},
	"zsh":  {},
	"ksh":  {},
	"dash": {},
}

// hasUnparseableShape reports whether segment uses a shape this parser cannot
// statically resolve. The conservative answer is "yes" so the rule can
// choose to default-deny.
func hasUnparseableShape(segment string) bool {
	if strings.Contains(segment, "$(") {
		return true
	}
	if strings.Contains(segment, "`") {
		return true
	}
	fields := shellFields(segment)
	if len(fields) == 0 {
		return false
	}
	argv0 := filepath.Base(fields[0])
	if _, present := alwaysUnparseable[argv0]; present {
		return true
	}
	if _, present := shellInterpreters[argv0]; present {
		if slices.Contains(fields[1:], "-c") {
			return true
		}
	}
	return false
}

func resolvePath(cwd, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if len(path) > 0 && path[0] == '~' {
		return path
	}
	if cwd == "" {
		return path
	}
	return filepath.Join(cwd, path)
}

// splitChain splits a command into chain segments at &&, ||, ;, |, and \n.
// This is a deliberately simplified split, intended to mirror the visibility
// of write targets across pipelines (a `tee` at the end of a pipeline is
// still a write target). The split keeps the chunk between the operators.
func splitChain(command string) []string {
	if command == "" {
		return nil
	}
	var segments []string
	var current strings.Builder
	runes := []rune(command)
	count := len(runes)
	for index := 0; index < count; index++ {
		r := runes[index]
		// Check two-char operators first.
		if index+1 < count {
			next := runes[index+1]
			if (r == '&' && next == '&') || (r == '|' && next == '|') {
				segments = append(segments, current.String())
				current.Reset()
				index++
				continue
			}
		}
		if r == ';' || r == '\n' || r == '|' {
			segments = append(segments, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		segments = append(segments, current.String())
	}
	return segments
}

// shellFields tokenizes s into fields using shell-style word splitting with
// quote and backslash handling. Whitespace separates fields outside quotes.
//
// This duplicates internal/rules/engine.go shellFields rather than calling
// it because this package cannot import internal/rules without creating an
// import cycle (engine.go itself imports concern packages). The function
// here is byte-for-byte equivalent in behavior to the engine helper for the
// inputs this package handles.
func shellFields(s string) []string {
	var fields []string
	var b strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if b.Len() == 0 {
			return
		}
		fields = append(fields, b.String())
		b.Reset()
	}

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return fields
}

// stripHeredocBodies removes the body of any heredoc blocks in command,
// leaving just the leading invocation line. This is a duplicate of the
// internal/rules/engine.go helper, repeated here for the same reason as
// shellFields above (avoiding an import cycle).
func stripHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	var out []string
	var pending []string

	for _, line := range lines {
		if len(pending) > 0 {
			if strings.TrimSpace(line) == pending[0] {
				pending = pending[1:]
			}
			continue
		}

		out = append(out, line)
		pending = append(pending, heredocDelimiters(line)...)
	}

	return strings.Join(out, "\n")
}

func heredocDelimiters(line string) []string {
	fields := shellFields(line)
	var out []string
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		switch {
		case field == "<<" || field == "<<-":
			if index+1 < len(fields) {
				out = append(out, fields[index+1])
				index++
			}
		case strings.HasPrefix(field, "<<-") && len(field) > len("<<-"):
			out = append(out, strings.TrimPrefix(field, "<<-"))
		case strings.HasPrefix(field, "<<") && len(field) > len("<<"):
			out = append(out, strings.TrimPrefix(field, "<<"))
		}
	}
	return out
}
