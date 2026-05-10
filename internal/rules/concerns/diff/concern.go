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
	ForEachStringGroupIndex(string, int, uint32, func(int, int) bool)
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
func EvalDiffMatches(fields FieldAccessor, pair config.FieldPairSpec, re Matcher, diagnosticGroup uint32, limit int) []MatchResult {
	if re == nil || limit <= 0 {
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
	newIndexes := collectIndexes(re, newText, diagnosticGroup, limit)
	if len(newIndexes) == 0 {
		return nil
	}
	oldStrings := findMatchingOldStrings(re, oldText, newIndexes, newText, diagnosticGroup)
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

func collectIndexes(re Matcher, text string, diagnosticGroup uint32, limit int) [][2]int {
	if text == "" || limit == 0 {
		return nil
	}
	indexes := make([][2]int, 0)
	re.ForEachStringGroupIndex(text, limit, diagnosticGroup, func(start int, end int) bool {
		indexes = append(indexes, [2]int{start, end})
		return true
	})
	return indexes
}

func findMatchingOldStrings(re Matcher, text string, newIndexes [][2]int, newText string, diagnosticGroup uint32) []string {
	if text == "" {
		return nil
	}

	pending := make(map[string]struct{}, len(newIndexes))
	for _, idx := range newIndexes {
		pending[newText[idx[0]:idx[1]]] = struct{}{}
	}
	if len(pending) == 0 {
		return nil
	}

	out := make([]string, 0, len(pending))
	re.ForEachStringGroupIndex(text, -1, diagnosticGroup, func(start int, end int) bool {
		matchedText := text[start:end]
		if _, ok := pending[matchedText]; !ok {
			return true
		}

		out = append(out, matchedText)
		delete(pending, matchedText)

		return len(pending) > 0
	})
	return out
}
