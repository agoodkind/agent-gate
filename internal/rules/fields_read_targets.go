package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
)

// CmdReadTargets returns the newline-joined effective filesystem targets of a
// code-search command (the paths it reads), scoped to the search tools the
// calling rule declares via search_tools. The tool list is rule policy, so
// there is no built-in default: an empty list yields no targets.
//
// The base (pre-cd) working directory is passed to ExtractCodeSearchTargets,
// which decomposes the whole command with shelldecomp and applies the cd chain
// itself, so `cd /other && grep -rn X .` is attributed to /other rather than the
// session cwd, and an unresolvable `cd "$VAR" && grep -rn X .` yields no
// resolvable target (shelldecomp cannot pin the cwd, so the operand is dropped
// rather than fabricated). Passing the base cwd, not effectiveCWD(), avoids
// applying the cd chain twice.
func (fields FieldSet) CmdReadTargets(searchTools []string) string {
	if len(searchTools) == 0 {
		return ""
	}
	if !fields.hasShellCommandContext() {
		return ""
	}
	command := fields.CommandValue()
	if command == "" {
		return ""
	}
	targets := shellread.ExtractCodeSearchTargets(command, fields.BaseCWD(), searchTools)
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Remote || target.Path == "" {
			continue
		}
		paths = append(paths, target.Path)
	}
	return strings.Join(paths, "\n")
}
