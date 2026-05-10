package diff_test

import (
	"strconv"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	intregex "goodkind.io/agent-gate/internal/regex"
	diffconcern "goodkind.io/agent-gate/internal/rules/concerns/diff"
	concernlimit "goodkind.io/agent-gate/internal/rules/concerns/limit"
)

// testFields is a minimal FieldAccessor for tests.
type testFields struct {
	values   map[config.FieldSelector]string
	filePath string
}

func (f *testFields) String(selector config.FieldSelector) string { return f.values[selector] }
func (f *testFields) FilePathValue() string                       { return f.filePath }

func newPair() config.FieldPairSpec {
	return config.FieldPairSpec{
		OldPath:  "tool_input.old_string",
		NewPath:  "tool_input.new_string",
		OldField: config.FieldToolInputOldString,
		NewField: config.FieldToolInputNewString,
	}
}

func compile(t *testing.T, pattern string) *intregex.Regexp {
	t.Helper()
	re, err := intregex.Compile(pattern)
	if err != nil {
		t.Fatalf("compile %q: %v", pattern, err)
	}
	return re
}

func TestEvalDiffMatches_NewOnlyBlocks(t *testing.T) {
	re := compile(t, `bad`)
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "good content",
			config.FieldToolInputNewString: "bad content added",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 1 {
		t.Fatalf("expected 1 additive match, got %d: %#v", len(got), got)
	}
	if got[0].Value != "bad content added" {
		t.Errorf("Value = %q, want %q", got[0].Value, "bad content added")
	}
	if matched := got[0].Value[got[0].Start:got[0].End]; matched != "bad" {
		t.Errorf("matched text = %q, want %q", matched, "bad")
	}
}

func TestEvalDiffMatches_PresentInBothAllows(t *testing.T) {
	re := compile(t, `bad`)
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "bad here",
			config.FieldToolInputNewString: "bad here too",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 0 {
		t.Fatalf("expected 0 matches when text appears in both, got %d: %#v", len(got), got)
	}
}

func TestEvalDiffMatches_PresentInOldOnlyAllows(t *testing.T) {
	re := compile(t, `bad`)
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "bad",
			config.FieldToolInputNewString: "clean",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 0 {
		t.Fatalf("expected 0 matches on deletion-only edit, got %d: %#v", len(got), got)
	}
}

func TestEvalDiffMatches_PresentInNeitherAllows(t *testing.T) {
	re := compile(t, `bad`)
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "clean",
			config.FieldToolInputNewString: "still clean",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 0 {
		t.Fatalf("expected 0 matches when pattern is in neither, got %d", len(got))
	}
}

func TestEvalDiffMatches_EmptyOldNonEmptyNewBlocks(t *testing.T) {
	re := compile(t, `bad`)
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "",
			config.FieldToolInputNewString: "this is bad",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 1 {
		t.Fatalf("expected 1 match when old is empty but pattern in new, got %d", len(got))
	}
}

func TestEvalDiffMatches_BatchEditsOneAdditive(t *testing.T) {
	// Pattern matches any "bad-N" id; each match is a unique text token, so
	// the string-set difference correctly isolates the new tokens.
	re := compile(t, `bad-\d+`)
	pair := config.FieldPairSpec{
		OldPath:  "edits[*].old_string",
		NewPath:  "edits[*].new_string",
		OldField: config.FieldEditsOldString,
		NewField: config.FieldEditsNewString,
	}
	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldEditsOldString: "edit one bad-1\nedit two clean",
			config.FieldEditsNewString: "edit one bad-1\nedit two bad-2",
		},
	}
	got := diffconcern.EvalDiffMatches(fields, pair, re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	// Only "bad-2" is additive; "bad-1" is in both views and is filtered.
	if len(got) != 1 {
		t.Fatalf("expected 1 additive match in batch, got %d: %#v", len(got), got)
	}
	if matched := got[0].Value[got[0].Start:got[0].End]; matched != "bad-2" {
		t.Errorf("matched text = %q, want bad-2", matched)
	}
}

func TestEvalDiffMatches_NilPatternReturnsNil(t *testing.T) {
	fields := &testFields{values: map[config.FieldSelector]string{}}
	if got := diffconcern.EvalDiffMatches(fields, newPair(), nil, 0, concernlimit.MaxCollectedMatchesPerEvaluation); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestEvalDiffMatches_CapsCollectedAdditiveMatches(t *testing.T) {
	re := compile(t, `bad-\d+`)

	var newBuilder strings.Builder
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			newBuilder.WriteByte(' ')
		}
		newBuilder.WriteString("bad-")
		newBuilder.WriteString(strconv.Itoa(i))
	}

	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: "",
			config.FieldToolInputNewString: newBuilder.String(),
		},
	}

	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != concernlimit.MaxCollectedMatchesPerEvaluation {
		t.Fatalf("expected %d additive matches, got %d", concernlimit.MaxCollectedMatchesPerEvaluation, len(got))
	}

	lastWant := "bad-" + strconv.Itoa(concernlimit.MaxCollectedMatchesPerEvaluation-1)
	lastGot := got[len(got)-1].Value[got[len(got)-1].Start:got[len(got)-1].End]
	if lastGot != lastWant {
		t.Fatalf("last retained additive match = %q, want %q", lastGot, lastWant)
	}
}

func TestEvalDiffMatches_ExcludesExistingTextNearEndOfOldScan(t *testing.T) {
	re := compile(t, `bad-\d+`)

	var oldBuilder strings.Builder
	for i := range concernlimit.MaxCollectedMatchesPerEvaluation + 32 {
		if i > 0 {
			oldBuilder.WriteByte(' ')
		}
		oldBuilder.WriteString("bad-9000")
	}
	oldBuilder.WriteString(" bad-5")

	fields := &testFields{
		values: map[config.FieldSelector]string{
			config.FieldToolInputOldString: oldBuilder.String(),
			config.FieldToolInputNewString: "bad-5 bad-6",
		},
	}

	got := diffconcern.EvalDiffMatches(fields, newPair(), re, 0, concernlimit.MaxCollectedMatchesPerEvaluation)
	if len(got) != 1 {
		t.Fatalf("expected 1 additive match after old-side filtering, got %d: %#v", len(got), got)
	}

	if matched := got[0].Value[got[0].Start:got[0].End]; matched != "bad-6" {
		t.Fatalf("matched text = %q, want bad-6", matched)
	}
}
