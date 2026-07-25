package config

import (
	"fmt"
	"strings"
)

func containsTemporalExecSelector(selectors []FieldSelectorSpec) bool {
	for _, selector := range selectors {
		if IsTemporalExecSelector(selector.Selector) {
			return true
		}
	}
	return false
}

func compileExecCacheKey(ruleName string, index int, condition *Condition) error {
	specs := CompileFieldSelectorSpecs([]string{condition.CacheKey})
	if len(specs) == 0 || specs[0].Selector == FieldSelectorInvalid {
		return fmt.Errorf(
			"rule %q condition %d: cache_key %q is not a valid field selector",
			ruleName,
			index,
			condition.CacheKey,
		)
	}
	condition.cacheKeySelector = specs[0]
	if IsTemporalExecSelector(condition.cacheKeySelector.Selector) {
		return fmt.Errorf(
			"rule %q condition %d: temporal selector %q is available only in exec field_paths",
			ruleName,
			index,
			condition.CacheKey,
		)
	}
	if condition.cacheKeySelector.Selector == FieldCmdReadTargets &&
		len(condition.SearchTools) == 0 {
		return fmt.Errorf(
			"rule %q condition %d: cache_key %q requires search_tools to declare which commands count as code search",
			ruleName,
			index,
			condition.CacheKey,
		)
	}
	return nil
}

func compileExecForEach(ruleName string, index int, condition *Condition) error {
	commandUsesItem := strings.Contains(strings.Join(condition.Command, "\x00"), "{{item}}")
	if commandUsesItem && strings.TrimSpace(condition.ForEach) == "" {
		return fmt.Errorf(
			"rule %q condition %d: command uses {{item}} but for_each is unset",
			ruleName,
			index,
		)
	}
	if strings.TrimSpace(condition.ForEach) == "" {
		if condition.MatchMode != "" {
			return fmt.Errorf(
				"rule %q condition %d: match_mode requires for_each",
				ruleName,
				index,
			)
		}
		return nil
	}

	specs := CompileFieldSelectorSpecs([]string{condition.ForEach})
	if len(specs) == 0 || specs[0].Selector == FieldSelectorInvalid {
		return fmt.Errorf(
			"rule %q condition %d: for_each %q is not a valid field selector",
			ruleName,
			index,
			condition.ForEach,
		)
	}
	condition.forEachSelector = specs[0]
	if IsTemporalExecSelector(condition.forEachSelector.Selector) {
		return fmt.Errorf(
			"rule %q condition %d: temporal selector %q is available only in exec field_paths",
			ruleName,
			index,
			condition.ForEach,
		)
	}
	if !commandUsesItem {
		return fmt.Errorf(
			"rule %q condition %d: for_each %q requires command to contain {{item}}",
			ruleName,
			index,
			condition.ForEach,
		)
	}
	if condition.MatchMode == "" {
		condition.MatchMode = ExecMatchAny
	}
	switch condition.MatchMode {
	case ExecMatchAny, ExecMatchAll:
	default:
		return fmt.Errorf(
			"rule %q condition %d: match_mode %q must be %q or %q",
			ruleName,
			index,
			condition.MatchMode,
			ExecMatchAny,
			ExecMatchAll,
		)
	}
	if condition.forEachSelector.Selector == FieldCmdReadTargets &&
		len(condition.SearchTools) == 0 {
		return fmt.Errorf(
			"rule %q condition %d: for_each %q requires search_tools to declare which commands count as code search",
			ruleName,
			index,
			condition.ForEach,
		)
	}
	return nil
}
