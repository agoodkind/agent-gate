package rules

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

func commandConditionCwds(fields FieldSet, c *config.Condition) ([]string, bool) {
	var matches []string
	for _, segment := range commandSegmentsWithCwd(fields) {
		fields := shellFields(segment.command)
		if c.StripEnv {
			fields = trimEnvAssignments(fields)
		}
		fields, cwd := normalizeCommandFields(fields, segment.cwd, c)
		if len(fields) == 0 {
			continue
		}

		argv0 := filepath.Base(fields[0])
		if c.Argv0 != "" && argv0 != c.Argv0 {
			continue
		}
		if len(c.Subcommands) == 0 {
			if !conditionTextMatch(strings.Join(fields[1:], " "), c) {
				continue
			}
			matches = append(matches, cwd)
			continue
		}
		if len(fields) > 1 && slices.Contains(c.Subcommands, fields[1]) {
			if !commandTailMatch(fields, c) {
				continue
			}
			matches = append(matches, cwd)
		}
	}

	return matches, len(matches) > 0
}

func commandTailMatch(fields []string, c *config.Condition) bool {
	if len(fields) < 2 {
		return conditionTextMatch("", c)
	}
	return conditionTextMatch(strings.Join(fields[1:], " "), c)
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

func normalizeCommandFields(fields []string, cwd string, c *config.Condition) ([]string, string) {
	fields, cwd = applyCwdFlags(fields, cwd, c.CwdFlags)
	for len(fields) > 0 && slices.Contains(c.StripArgs, filepath.Base(fields[0])) {
		fields = fields[1:]
		if c.StripEnv {
			fields = trimEnvAssignments(fields)
		}
		fields, cwd = applyCwdFlags(fields, cwd, c.CwdFlags)
	}
	return fields, cwd
}

func applyCwdFlags(fields []string, cwd string, flags []string) ([]string, string) {
	if len(fields) == 0 || len(flags) == 0 {
		return fields, cwd
	}

	out := make([]string, 0, len(fields))
	out = append(out, fields[0])
	for i := 1; i < len(fields); i++ {
		field := fields[i]
		if _, value, ok := splitCwdFlag(field, flags); ok {
			if value == "" && i+1 < len(fields) {
				value = fields[i+1]
				i++
			}
			if value != "" {
				cwd = resolvePath(cwd, value)
			}
			continue
		}
		out = append(out, field)
	}
	return out, cwd
}

func splitCwdFlag(field string, flags []string) (string, string, bool) {
	for _, flag := range flags {
		if field == flag {
			return flag, "", true
		}
		if after, ok := strings.CutPrefix(field, flag+"="); ok {
			return flag, after, true
		}
	}
	return "", "", false
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

type commandSegment struct {
	command string
	cwd     string
}

func commandSegmentsWithCwd(fields FieldSet) []commandSegment {
	cwd := fields.BaseCWD()
	if cwd == "" {
		return nil
	}

	cmd := fields.CommandValue()
	if cmd == "" {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = cwd
	}

	cmd = stripHeredocBodies(cmd)

	var out []commandSegment
	for _, seg := range cmdChainRe.Split(cmd, -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		out = append(out, commandSegment{command: seg, cwd: cwd})
		if next, ok := cdTarget(cwd, home, seg); ok {
			cwd = next
		}
	}
	return out
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
