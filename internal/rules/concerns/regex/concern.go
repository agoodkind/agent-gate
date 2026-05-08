// Package regex provides field-level regex evaluation helpers used by the rules engine.
package regex

import (
	"goodkind.io/agent-gate/internal/config"
)

// Matcher is the subset of *regex.Regexp used by field evaluation.
// It is exported so callers can pass compiled patterns without importing regex internals.
type Matcher interface {
	FindAllStringGroupIndex(string, int, uint32) [][2]int
}

// FieldAccessor allows the package to read field values and the file path
// without importing the rules package and causing an import cycle.
type FieldAccessor interface {
	String(selector config.FieldSelector) string
	FilePathValue() string
}

// MatchResult records one concrete regex match.
type MatchResult struct {
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
}

// EvalFieldMatches evaluates re against every field selected by selectors and
// returns one MatchResult per match position found.
func EvalFieldMatches(fields FieldAccessor, selectors []config.FieldSelectorSpec, re Matcher, diagnosticGroup int) []MatchResult {
	var matches []MatchResult
	filePath := fields.FilePathValue()
	for _, selector := range selectors {
		value := fields.String(selector.Selector)
		if value == "" {
			continue
		}
		for _, idx := range re.FindAllStringGroupIndex(value, -1, uint32(diagnosticGroup)) {
			matches = append(matches, MatchResult{
				FieldPath: selector.Path,
				FilePath:  filePath,
				Value:     value,
				Start:     idx[0],
				End:       idx[1],
			})
		}
	}
	return matches
}

// ConditionMatch reports whether the regex condition c passes for fields.
// A condition passes when:
//   - Pattern is unset or matches the extracted value, AND
//   - NotPattern is unset or does NOT match the extracted value.
func ConditionMatch(fields FieldAccessor, c *config.Condition) bool {
	var value string
	for _, selector := range c.Selectors() {
		v := fields.String(selector.Selector)
		if v != "" {
			value = v
			break
		}
	}
	if c.CompiledPattern() != nil && !c.CompiledPattern().MatchString(value) {
		return false
	}
	if c.CompiledNotPattern() != nil && c.CompiledNotPattern().MatchString(value) {
		return false
	}
	return true
}
