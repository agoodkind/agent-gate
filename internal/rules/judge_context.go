package rules

import (
	"context"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
)

// judgeContextFact is one resolved label and value rendered into the judge input
// panel. Value is the empty string when the fact resolved to nothing; the
// renderer shows a placeholder so the judge sees the fact was requested.
// FromCheckoutStatus marks a fact produced by the checkout_status key, so the
// panel can suppress the raw working-directory lines the labeled fact replaces.
type judgeContextFact struct {
	Label              string
	Value              string
	FromCheckoutStatus bool
}

// resolveJudgeContext resolves the union of judge_context keys the participating
// rules requested into labeled facts for the judge panel. A field-path key is
// resolved through the same FieldSet accessor the deterministic conditions use.
// The computed checkout_status key labels the effective working directory and
// each resolved write target by its git checkout role. Reads of git state use
// the reader from ctx, so this runs in the batch caller, not in the pure panel
// builder.
func resolveJudgeContext(ctx context.Context, fields FieldSet, keys []string) []judgeContextFact {
	facts := make([]judgeContextFact, 0, len(keys))
	seen := make(map[string]bool, len(keys))
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		if trimmed == config.JudgeContextCheckoutStatus {
			facts = append(facts, resolveCheckoutStatus(ctx, fields)...)
			continue
		}
		selector := config.CompileFieldSelector(trimmed)
		if selector == config.FieldSelectorInvalid {
			continue
		}
		facts = append(facts, judgeContextFact{Label: trimmed, Value: fields.String(selector), FromCheckoutStatus: false})
	}
	return facts
}

// resolveCheckoutStatus labels the effective working directory and reports which
// resolved write targets fall inside the primary checkout, so the judge decides
// from a stated fact rather than re-deriving it from raw directory strings.
func resolveCheckoutStatus(ctx context.Context, fields FieldSet) []judgeContextFact {
	read := gitStateReaderFromContext(ctx)
	facts := make([]judgeContextFact, 0, 2)

	effective := strings.TrimSpace(fields.effectiveCWD())
	if effective != "" {
		facts = append(facts, judgeContextFact{
			Label:              "checkout_status effective_cwd",
			Value:              checkoutLabel(read, effective),
			FromCheckoutStatus: true,
		})
	}

	writeTargets := splitNonEmptyLines(fields.CmdWriteTargets())
	underPrimary := make([]string, 0, len(writeTargets))
	for _, target := range writeTargets {
		if pathIsPrimaryCheckout(read, target) {
			underPrimary = append(underPrimary, target)
		}
	}
	var value string
	switch {
	case len(writeTargets) == 0:
		value = "none resolved (the command has no statically resolved write target)"
	case len(underPrimary) == 0:
		value = "none"
	default:
		value = strings.Join(underPrimary, ", ")
	}
	facts = append(facts, judgeContextFact{
		Label:              "write targets under the primary checkout",
		Value:              value,
		FromCheckoutStatus: true,
	})
	return facts
}

// checkoutLabel classifies a directory as the primary checkout, a linked
// worktree, or outside a git repository. An unresolved read is reported as
// outside a repository, matching the fail-open contract the conditions use.
func checkoutLabel(read gitStateReader, path string) string {
	state, err := read(path)
	if err != nil {
		return "not in a git repository"
	}
	if gitbranch.IsPrimaryCheckout(state, path) {
		return "the primary checkout"
	}
	return "a linked worktree, not the primary checkout"
}

// pathIsPrimaryCheckout reports whether a write target resolves inside the
// primary checkout. An unresolved read reports false, so an unreadable path is
// never labeled a primary-checkout write.
func pathIsPrimaryCheckout(read gitStateReader, path string) bool {
	state, err := read(path)
	if err != nil {
		return false
	}
	return gitbranch.IsPrimaryCheckout(state, path)
}

// splitNonEmptyLines splits a newline-joined value into its non-empty trimmed
// lines.
func splitNonEmptyLines(value string) []string {
	lines := strings.Split(value, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
