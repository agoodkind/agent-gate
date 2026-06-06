// Package regex provides field-level regex evaluation helpers used by the rules engine.
package regex

import (
	"math"

	"goodkind.io/agent-gate/internal/config"
)

// Matcher is the subset of *regex.Regexp used by field evaluation.
// It is exported so callers can pass compiled patterns without importing regex internals.
type Matcher interface {
	FindAllStringGroupIndex(string, int, uint32) [][2]int
	ForEachStringGroupIndex(string, int, uint32, func(int, int) bool)
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
func EvalFieldMatches(fields FieldAccessor, selectors []config.FieldSelectorSpec, re Matcher, diagnosticGroup int, limit int) []MatchResult {
	if limit <= 0 {
		return nil
	}

	group := uint32(0)
	if diagnosticGroup > 0 && diagnosticGroup <= math.MaxUint32 {
		group = uint32(diagnosticGroup)
	}

	var matches []MatchResult
	filePath := fields.FilePathValue()
	remaining := limit
	for _, selector := range selectors {
		if remaining == 0 {
			break
		}
		value := fields.String(selector.Selector)
		if value == "" {
			continue
		}
		re.ForEachStringGroupIndex(value, remaining, group, func(start int, end int) bool {
			matches = append(matches, MatchResult{
				FieldPath: selector.Path,
				FilePath:  filePath,
				Value:     value,
				Start:     start,
				End:       end,
			})
			remaining--
			return remaining > 0
		})
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
