package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/rules/concerns/shellwrite"
)

// CmdWriteTargets returns the newline-joined effective filesystem targets that
// the active shell command writes (output redirects, tee, dd of=, sed -i,
// awk -i, patch, git apply), resolved against the cwd in effect at each write. It is the
// write-side mirror of [FieldSet.CmdReadTargets] and delegates to
// shellwrite.ExtractWriteTargets (structural shelldecomp parse, no regex).
// Sentinel targets for unparseable write shapes carry no path and are dropped;
// a rule that needs to default-deny those uses a shell_write condition instead.
func (fields FieldSet) CmdWriteTargets() string {
	if !fields.hasShellCommandContext() {
		return ""
	}
	command := fields.CommandValue()
	if command == "" {
		return ""
	}
	targets := shellwrite.ExtractWriteTargets(command, fields.BaseCWD())
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		if target.Reason != shellwrite.ReasonOK || target.Path == "" {
			continue
		}
		paths = append(paths, target.Path)
	}
	return strings.Join(paths, "\n")
}
