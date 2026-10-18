package rules

import (
	"slices"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
)

type sourcePathPattern struct {
	pattern    *regex.Regexp
	notPattern *regex.Regexp
}

// sourceReadPolicy classifies helper paths with the active selector patterns
// before source inspection. Other rule conditions still govern the final verdict.
// Direct path matching avoids recursively evaluating the source selector.
func sourceReadPolicy(rulesSlice []config.Rule, system, eventName string, getenv func(string) string) func(string) bool {
	var patterns []sourcePathPattern
	for index := range rulesSlice {
		rule := &rulesSlice[index]
		if !appliesToEvent(rule, system, eventName) || envGuardFires(getenv, rule.DisableIfEnv) {
			continue
		}
		if len(rule.Conditions) == 0 {
			patterns = appendSourcePattern(patterns, rule.Selectors(), rule.Compiled(), rule.CompiledNot())
			continue
		}
		for conditionIndex := range rule.Conditions {
			condition := &rule.Conditions[conditionIndex]
			if conditionKind(condition) != config.ConditionKindRegex {
				continue
			}
			patterns = appendSourcePattern(patterns, condition.Selectors(), condition.CompiledPattern(), condition.CompiledNotPattern())
		}
	}
	return func(path string) bool {
		for _, pattern := range patterns {
			if pattern.pattern != nil && !pattern.pattern.MatchString(path) {
				continue
			}
			if pattern.notPattern == nil || !pattern.notPattern.MatchString(path) {
				return true
			}
		}
		return false
	}
}

func appendSourcePattern(patterns []sourcePathPattern, selectors []config.FieldSelectorSpec, pattern, notPattern *regex.Regexp) []sourcePathPattern {
	if pattern == nil && notPattern == nil {
		return patterns
	}
	if slices.ContainsFunc(selectors, func(selector config.FieldSelectorSpec) bool {
		return selector.Selector == config.FieldCredentialReadTargets
	}) {
		return append(patterns, sourcePathPattern{pattern: pattern, notPattern: notPattern})
	}
	return patterns
}
