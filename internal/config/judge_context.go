package config

import (
	"fmt"
	"strings"
)

// JudgeContextCheckoutStatus is the computed judge_context key that labels the
// effective working directory and each resolved write target as the primary
// checkout, a linked worktree, or outside a git repository. Every other key is a
// field path resolved by CompileFieldSelector.
const JudgeContextCheckoutStatus = "checkout_status"

// validateRuleJudgeContext rejects a judge_context key that is neither the
// computed checkout_status key nor a known field path, so a typo fails the config
// load instead of silently injecting nothing into the judge panel.
func validateRuleJudgeContext(ruleName string, keys []string) error {
	for _, key := range keys {
		trimmed := strings.TrimSpace(key)
		if trimmed == JudgeContextCheckoutStatus {
			continue
		}
		if CompileFieldSelector(trimmed) != FieldSelectorInvalid {
			continue
		}
		return fmt.Errorf(
			"rule %q judge_context %q: not a known field path or %q",
			ruleName, key, JudgeContextCheckoutStatus,
		)
	}
	return nil
}
