package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
)

// gitDefaultBranchConditionMatch reports whether any target the operation acts
// on lives in a git repo whose HEAD is the default branch. Targets come from the
// resolved command cwds when a command condition is present (a git verb's
// -C/cd/process-cwd repo), otherwise from the condition's field selectors (an
// edit's file_path, or cmd_write_targets for a shell write). This mirrors how
// projectConditionMatch sources its directories, so the decision is the branch
// of the affected repo, never the shell's cwd shape. A detached or unresolved
// repo never matches, so a block built on this condition fails open. It runs
// only after the cheaper conditions in the rule already matched (a git command,
// or an edit tool), so the go-git open happens on candidate events only.
func gitDefaultBranchConditionMatch(fields FieldSet, c *config.Condition, ctx conditionContext) bool {
	for _, target := range gitBranchTargets(fields, c, ctx) {
		if match, resolved := gitbranch.OnDefaultBranch(target); resolved && match {
			return true
		}
	}
	return false
}

// gitBranchTargets returns the deduplicated set of filesystem targets to test:
// the resolved command cwds when present, else every non-empty line of every
// configured selector value.
func gitBranchTargets(fields FieldSet, c *config.Condition, ctx conditionContext) []string {
	if len(ctx.commandCwds) > 0 {
		return dedupeNonEmpty(ctx.commandCwds)
	}
	var targets []string
	for _, spec := range c.Selectors() {
		value := fields.String(spec.Selector)
		if value == "" {
			continue
		}
		targets = append(targets, strings.Split(value, "\n")...)
	}
	return dedupeNonEmpty(targets)
}

func dedupeNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
