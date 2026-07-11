package rules

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
	"goodkind.io/gksyntax/shelldecomp"
)

// commandConditionCwds matches a command condition against the shell AST that
// gksyntax exposes. It consumes shelldecomp.Commands() directly (each with a
// resolved argv0, a cd-resolved Cwd, and Args classified as flag or literal
// words) rather than re-tokenizing, so the git subcommand is interpreted from
// the word classification instead of read positionally. It returns the resolved
// cwds of the matching commands.
func commandConditionCwds(fields FieldSet, c *config.Condition) ([]string, bool) {
	command := fields.CommandValue()
	base := fields.BaseCWD()
	if command == "" || base == "" {
		return nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = base
	}
	// Parse the raw command: shelldecomp handles heredocs natively (the body is an
	// embedded region, not split on), so no pre-stripping is needed.
	decomposition := shelldecomp.Parse(shellparse.ExpandLiteralAssignments(command), base, home)

	var matches []string
	for _, parsed := range decomposition.Commands() {
		argv0 := filepath.Base(parsed.Argv0)
		words := parsed.Args
		argv0, words = stripCommandWrappers(argv0, words, c.StripArgs)
		if c.Argv0 != "" && argv0 != c.Argv0 {
			continue
		}
		cwd := parsed.Cwd
		if cwd == "" {
			cwd = base
		}
		var cwdResolved bool
		cwd, words, cwdResolved = applyCwdFlagWords(cwd, words, c.CwdFlags)
		if !cwdResolved {
			continue
		}
		if len(c.Subcommands) == 0 {
			if conditionTextMatch(wordsTail(words), c) {
				matches = append(matches, cwd)
			}
			continue
		}
		// Normalize the tail to start at the resolved subcommand so an anchored
		// pattern (for example ^commit) is not defeated by leading global flags
		// like `git -c name=value` or `git -C path` that precede the subcommand.
		if sub, idx := commandSubcommand(argv0, words); sub != "" && slices.Contains(c.Subcommands, sub) {
			if conditionTextMatch(wordsTail(words[idx:]), c) {
				matches = append(matches, cwd)
			}
		}
	}
	return matches, len(matches) > 0
}

// gitGlobalValueFlags are git's pre-subcommand global options that take the
// following word as a separate value (git -c <name=value>, git -C <path>, and
// the space forms of --git-dir/--work-tree/etc.). The subcommand scan skips both
// the flag word and its value word. The =form (--git-dir=/x) is a single flag
// word handled by the generic flag skip.
var gitGlobalValueFlags = map[string]bool{
	"-c": true, "-C": true, "--exec-path": true, "--git-dir": true,
	"--work-tree": true, "--namespace": true, "--super-prefix": true,
	"--config-env": true, "--attr-source": true,
}

// commandSubcommand interprets the effective subcommand from the AST's
// flag/literal word classification rather than assuming the word right after
// argv0. It skips the leading flag words (and, for git, the value word of a
// value-taking global like -c) and returns the first literal word with its
// index in words. This makes `git -c user.email=a commit` interpret to
// `commit`, which a positional read (which sees `-c`) misses. The index lets a
// caller normalize the command tail from the subcommand onward. The empty
// string with index -1 means no subcommand.
func commandSubcommand(argv0 string, words []shelldecomp.Word) (string, int) {
	isGit := argv0 == "git"
	for i := 0; i < len(words); i++ {
		word := words[i]
		if word.Kind == shelldecomp.WordKindFlag {
			if isGit && gitGlobalValueFlags[word.Value] {
				i++ // the flag's value is a separate word; skip it too
			}
			continue
		}
		return word.Value, i
	}
	return "", -1
}

// stripCommandWrappers removes the configured wrapper argv0s (time, command,
// doas, xargs) that shelldecomp keeps as the command's argv0, promoting the next
// word to argv0. shelldecomp already drops env assignments and sudo, so those
// need no handling here.
func stripCommandWrappers(argv0 string, words []shelldecomp.Word, stripArgs []string) (string, []shelldecomp.Word) {
	for slices.Contains(stripArgs, argv0) && len(words) > 0 {
		// Skip the wrapper's own leading option words (time -p, command -p) and
		// the -- end-of-options marker so argv0 lands on the wrapped command
		// rather than an option like -p, which would defeat the argv0 match.
		i := 0
		for i < len(words) && (words[i].Kind == shelldecomp.WordKindFlag || words[i].Value == "--") {
			i++
		}
		if i == len(words) {
			break // nothing but options after the wrapper; leave argv0 as the wrapper
		}
		argv0 = filepath.Base(words[i].Value)
		words = words[i+1:]
	}
	return argv0, words
}

// applyCwdFlagWords resolves a cwd-redirecting flag (for example git/make -C, or
// swift --package-path) from the command's words, returning the redirected cwd
// and the words with the flag (and its separate value) removed.
func applyCwdFlagWords(cwd string, words []shelldecomp.Word, flags []string) (string, []shelldecomp.Word, bool) {
	if len(flags) == 0 {
		return cwd, words, true
	}
	out := make([]shelldecomp.Word, 0, len(words))
	for i := 0; i < len(words); i++ {
		word := words[i]
		// Only a flag word can be a cwd redirect: a quoted literal whose value
		// happens to equal a cwd flag (a filename "-C") must not be treated as one.
		value, ok := "", false
		if word.Kind == shelldecomp.WordKindFlag {
			value, ok = splitCwdFlag(word.Value, flags)
		}
		if !ok {
			out = append(out, word)
			continue
		}
		if !word.Resolvable {
			return "", nil, false
		}
		if value == "" && i+1 < len(words) {
			if !words[i+1].Resolvable {
				return "", nil, false
			}
			value = words[i+1].Value
			i++
		}
		if value != "" {
			cwd = resolvePath(cwd, value)
		}
	}
	return cwd, out, true
}

// wordsTail joins the resolved values of a command's words for pattern matching,
// matching the unquoted form the condition patterns expect.
func wordsTail(words []shelldecomp.Word) string {
	parts := make([]string, len(words))
	for i, word := range words {
		parts[i] = word.Value
	}
	return strings.Join(parts, " ")
}

func conditionTextMatch(value string, c *config.Condition) bool {
	if re := c.CompiledPattern(); re != nil {
		if !re.MatchString(value) {
			return false
		}
	} else if c.Pattern != "" {
		re, err := regex.Compile(c.Pattern)
		if err != nil || !re.MatchString(value) {
			return false
		}
	}

	if re := c.CompiledNotPattern(); re != nil {
		if re.MatchString(value) {
			return false
		}
	} else if c.NotPattern != "" {
		re, err := regex.Compile(c.NotPattern)
		if err != nil || re.MatchString(value) {
			return false
		}
	}

	return true
}

// splitCwdFlag reports whether field is one of the cwd-redirecting flags and, if
// so, returns the flag's inline value (empty when the value is a separate word).
func splitCwdFlag(field string, flags []string) (string, bool) {
	for _, flag := range flags {
		if field == flag {
			return "", true
		}
		if after, ok := strings.CutPrefix(field, flag+"="); ok {
			return after, true
		}
	}
	return "", false
}

func resolvePath(cwd, path string) string {
	switch {
	case len(path) == 1 && path[0] == '~':
		home, err := os.UserHomeDir()
		if err != nil {
			return cwd
		}
		return home
	case len(path) >= 2 && path[0] == '~' && path[1] == '/':
		home, err := os.UserHomeDir()
		if err != nil {
			return cwd
		}
		return filepath.Join(home, path[2:])
	case filepath.IsAbs(path):
		return path
	default:
		return filepath.Join(cwd, path)
	}
}

func projectConditionMatch(fields FieldSet, c *config.Condition, ctx conditionContext) bool {
	cwds := ctx.commandCwds
	if len(cwds) == 0 {
		if cwd := fields.String(config.FieldEffectiveCWD); cwd != "" {
			cwds = []string{cwd}
		}
	}
	if len(cwds) == 0 {
		return false
	}

	for _, cwd := range cwds {
		if projectConditionMatchCwd(cwd, c) {
			return true
		}
	}
	return false
}

func projectConditionMatchCwd(cwd string, c *config.Condition) bool {
	root := cwd
	if len(c.RootMarkers) > 0 {
		found, ok := findProjectRoot(cwd, c.RootMarkers)
		if !ok {
			return false
		}
		root = found
	}

	if len(c.RequireAny) > 0 && !anyPathExists(root, c.RequireAny) {
		return false
	}
	if len(c.RequireAll) > 0 && !allPathsExist(root, c.RequireAll) {
		return false
	}
	if len(c.ForbidAny) > 0 && anyPathExists(root, c.ForbidAny) {
		return false
	}

	return true
}

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
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		switch {
		case field == "<<" || field == "<<-":
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
				i++
			}
		case strings.HasPrefix(field, "<<-") && len(field) > len("<<-"):
			out = append(out, strings.TrimPrefix(field, "<<-"))
		case strings.HasPrefix(field, "<<") && len(field) > len("<<"):
			out = append(out, strings.TrimPrefix(field, "<<"))
		}
	}
	return out
}

func findProjectRoot(start string, markers []string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if anyPathExists(dir, markers) {
			return dir, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func anyPathExists(root string, paths []string) bool {
	for _, path := range paths {
		if pathExists(filepath.Join(root, path)) {
			return true
		}
	}
	return false
}

func allPathsExist(root string, paths []string) bool {
	for _, path := range paths {
		if !pathExists(filepath.Join(root, path)) {
			return false
		}
	}
	return true
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func trimEnvAssignments(fields []string) []string {
	for len(fields) > 0 && isEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	return fields
}

func isEnvAssignment(s string) bool {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return false
	}
	for j, r := range s[:i] {
		if j == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

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
