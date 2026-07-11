package rules

import (
	"path/filepath"
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
// repo never matches, so a block built on this condition fails open. allConditionsMatch
// evaluates conditions in config order, so place this condition after the cheaper
// gate (an edit-tool regex or a git command condition) in the rule; that preceding
// gate then short-circuits and the go-git open runs on candidate events only.
func gitDefaultBranchConditionMatch(fields FieldSet, c *config.Condition, ctx conditionContext) bool {
	for _, target := range gitBranchTargets(fields, c, ctx) {
		if match, resolved := gitbranch.OnDefaultBranch(target); resolved && match {
			return true
		}
	}
	return false
}

// gitBranchTargets returns the deduplicated set of filesystem targets to test:
// the resolved command cwds (a git verb's -C/cd/process-cwd repo) merged with
// every non-empty line of every configured selector value. Both sources are
// used, so a rule that pairs a command condition with a file selector checks the
// repos of both. A relative selector value (a provider may pass a relative
// tool_input.file_path straight through) is resolved against the event cwd first,
// so a relative target is enforced rather than silently skipped.
func gitBranchTargets(fields FieldSet, c *config.Condition, ctx conditionContext) []string {
	base := fields.BaseCWD()
	targets := make([]string, 0, len(ctx.commandCwds))
	targets = append(targets, ctx.commandCwds...)
	for _, spec := range c.Selectors() {
		value := fields.StringForCondition(spec.Selector, c)
		if value == "" {
			continue
		}
		for line := range strings.SplitSeq(value, "\n") {
			if line == "" || strings.ContainsRune(line, 0) {
				continue
			}
			if !filepath.IsAbs(line) {
				if base == "" {
					continue
				}
				line = filepath.Join(base, line)
			}
			targets = append(targets, line)
		}
	}
	return dedupeUsable(targets)
}

// dedupeUsable returns the distinct usable target paths. It drops empties, the
// shelldecomp unresolvable sentinel (which carries a NUL byte), and any
// remaining non-absolute value, so an unpinnable cwd or write target can never
// collapse to "." inside gitbranch.OnDefaultBranch and accidentally evaluate the
// daemon's own directory. This preserves the fail-open contract for unresolvable
// targets while gitBranchTargets has already resolved relative selector paths.
func dedupeUsable(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) {
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
