// Package diff provides a Concern that fires only when a regex pattern matches
// the "new" side of a field pair without matching the "old" side. This lets a
// rule treat additive content as a violation while ignoring identity-preserving
// edits and pure deletions.
package diff

import (
	"goodkind.io/agent-gate/internal/config"
)

// Matcher is the subset of *regex.Regexp used by diff evaluation.
type Matcher interface {
	FindAllStringGroupIndex(string, int, uint32) [][2]int
}

// FieldAccessor allows the package to read field values without importing
// the rules package and creating an import cycle.
type FieldAccessor interface {
	String(selector config.FieldSelector) string
	FilePathValue() string
}

// MatchResult records one concrete additive match.
type MatchResult struct {
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
}

// EvalDiffMatches returns the set of regex matches present in the New side of
// the field pair but absent from the Old side. Comparison uses the captured
// match text, not byte offsets, so identity-preserving rewrites (where the
// same text exists in both old and new) do not produce a violation.
//
// For batch edits where the field pair resolves to joined per-edit values
// (for example "edits[*].old_string" / "edits[*].new_string"), the same
// text-based comparison applies: a match is additive only if its text is
// not present anywhere in the old joined view.
func EvalDiffMatches(fields FieldAccessor, pair config.FieldPairSpec, re Matcher, diagnosticGroup uint32) []MatchResult {
	if re == nil {
		return nil
	}
	if pair.NewField == config.FieldSelectorInvalid {
		return nil
	}
	newText := fields.String(pair.NewField)
	if newText == "" {
		return nil
	}
	oldText := fields.String(pair.OldField)
	newIndexes := re.FindAllStringGroupIndex(newText, -1, diagnosticGroup)
	if len(newIndexes) == 0 {
		return nil
	}
	oldStrings := matchStrings(re, oldText, diagnosticGroup)
	additive := NewOnly(newIndexes, oldStrings, newText)
	if len(additive) == 0 {
		return nil
	}
	filePath := fields.FilePathValue()
	out := make([]MatchResult, len(additive))
	for i, idx := range additive {
		out[i] = MatchResult{
			FieldPath: pair.NewPath,
			FilePath:  filePath,
			Value:     newText,
			Start:     idx[0],
			End:       idx[1],
		}
	}
	return out
}

func matchStrings(re Matcher, text string, diagnosticGroup uint32) []string {
	if text == "" {
		return nil
	}
	indexes := re.FindAllStringGroupIndex(text, -1, diagnosticGroup)
	if len(indexes) == 0 {
		return nil
	}
	out := make([]string, len(indexes))
	for i, idx := range indexes {
		out[i] = text[idx[0]:idx[1]]
	}
	return out
}
