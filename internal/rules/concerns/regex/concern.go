// Package regex implements the pipeline.Concern for regex-based rule evaluation.
package regex

import (
	"context"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/pipeline"
)

// RegexMatcher is the subset of *regex.Regexp used by field evaluation.
// It is exported so callers can pass compiled patterns without importing regex internals.
type RegexMatcher interface {
	FindAllStringGroupIndex(string, int, uint32) [][2]int
}

// FieldAccessor allows the Concern to read field values and the file path
// without importing the rules package and causing an import cycle.
type FieldAccessor interface {
	String(selector config.FieldSelector) string
	FilePathValue() string
}

// MatchResult records one concrete regex match returned by the Concern.
type MatchResult struct {
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
}

// Input carries the compiled rule state and field accessor for one evaluation.
type Input struct {
	RuleName        string
	Selectors       []config.FieldSelectorSpec
	CompiledPattern RegexMatcher
	DiagnosticGroup int
	Fields          FieldAccessor
}

// Outcome holds the matches produced by one Concern execution.
type Outcome struct {
	Matches []MatchResult
}

// Concern evaluates one regex pattern against a set of field selectors.
type Concern struct {
	ruleName string
	profile  pipeline.Profile
}

// New constructs a Concern for the given rule name. The rule name appears
// in the Profile so the orchestrator can attribute results.
func New(ruleName string) *Concern {
	return &Concern{
		ruleName: ruleName,
		profile: pipeline.Profile{
			Name:         ruleName,
			Cost:         pipeline.CostCheap,
			Idempotent:   true,
			MemoLifetime: pipeline.MemoEvent,
		},
	}
}

// Profile returns the static execution profile for this Concern.
func (c *Concern) Profile() pipeline.Profile {
	return c.profile
}

// Execute runs the regex match over the fields carried in in.
// It returns an Outcome containing all matching positions, or an empty Outcome
// when nothing matches.
func (c *Concern) Execute(_ context.Context, in pipeline.Input) (pipeline.Outcome, error) {
	ri, ok := in.(Input)
	if !ok {
		return Outcome{}, nil
	}
	matches := executeFieldRegex(ri.Fields, ri.Selectors, ri.CompiledPattern, ri.DiagnosticGroup)
	return Outcome{Matches: matches}, nil
}

// EvalFieldMatches is the exported form of executeFieldRegex, callable from
// engine.go without going through Execute.
func EvalFieldMatches(fields FieldAccessor, selectors []config.FieldSelectorSpec, re RegexMatcher, diagnosticGroup int) []MatchResult {
	return executeFieldRegex(fields, selectors, re, diagnosticGroup)
}

// executeFieldRegex iterates over each selector, extracts the field value, and
// appends a MatchResult for every match the compiled pattern finds.
func executeFieldRegex(fields FieldAccessor, selectors []config.FieldSelectorSpec, re RegexMatcher, diagnosticGroup int) []MatchResult {
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

// ConditionMatchChecker is satisfied by *config.Condition and provides the
// pattern fields needed by ConditionMatch.
type ConditionMatchChecker interface {
	Selectors() []config.FieldSelectorSpec
	CompiledPattern() patternMatcher
	CompiledNotPattern() patternMatcher
}

// patternMatcher is the subset of *regex.Regexp used for condition matching.
type patternMatcher interface {
	MatchString(string) bool
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
