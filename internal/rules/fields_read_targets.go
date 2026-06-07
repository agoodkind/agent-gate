package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
)

// CmdReadTargets returns the newline-joined effective filesystem targets of a
// grep/rg-style command (the paths it reads), so an exec gate can scope its
// decision to what a search actually reads rather than the working directory.
func (fields FieldSet) CmdReadTargets() string {
	if !fields.hasShellCommandContext() {
		return ""
	}
	command := fields.CommandValue()
	if command == "" {
		return ""
	}
	targets := shellread.ExtractCodeSearchTargets(command, fields.BaseCWD())
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Remote || target.Path == "" {
			continue
		}
		paths = append(paths, target.Path)
	}
	return strings.Join(paths, "\n")
}
