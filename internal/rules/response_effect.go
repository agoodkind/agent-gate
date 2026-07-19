package rules

import "goodkind.io/agent-gate/internal/config"

// ResponseEffect is a typed model-facing action produced by the same rule
// evaluation pass that produces enforcement violations. Output is kept only in
// memory for response rendering and must never be logged verbatim.
type ResponseEffect struct {
	RuleName    string
	Action      string
	Output      string
	Disposition string
}

// Response effect dispositions are intentionally content-free audit labels.
const (
	ResponseEffectApplied     = "applied"
	ResponseEffectEmptyNoOp   = "empty_noop"
	ResponseEffectErroredNoOp = "errored_noop"
)

func responseEffectsFromMatches(
	violations []Violation,
	rulesSlice []config.Rule,
	memo *execEventMemo,
) ([]Violation, []ResponseEffect) {
	matched := make(map[string]struct{})
	for _, violation := range violations {
		matched[violation.RuleName] = struct{}{}
	}
	filtered := make([]Violation, 0, len(violations))
	effects := make([]ResponseEffect, 0)
	for index := range rulesSlice {
		rule := &rulesSlice[index]
		if _, ok := matched[rule.Name]; !ok {
			continue
		}
		if !rule.IsResponseAction() {
			continue
		}
		output := rule.OutputText()
		disposition := ResponseEffectApplied
		if memo != nil {
			if memo.erroredFor(rule.Name) {
				output = ""
				disposition = ResponseEffectErroredNoOp
			}
			if dynamicOutput, ok := memo.outputFor(rule.Name); ok {
				if disposition != ResponseEffectErroredNoOp {
					output = dynamicOutput
				}
			}
		}
		if output == "" && disposition == ResponseEffectApplied {
			disposition = ResponseEffectEmptyNoOp
		}
		effects = append(effects, ResponseEffect{
			RuleName: rule.Name, Action: rule.Action, Output: output, Disposition: disposition,
		})
	}
	for _, violation := range violations {
		for index := range rulesSlice {
			rule := &rulesSlice[index]
			if rule.Name == violation.RuleName && rule.IsResponseAction() {
				goto nextViolation
			}
		}
		filtered = append(filtered, violation)
	nextViolation:
	}
	return filtered, effects
}
