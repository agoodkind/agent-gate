package config

import (
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"
)

// TestTableHeaderSpellings is the regression for a presence check that missed
// valid TOML. Only the exact string "[update]" was recognized, so a config
// written "[update] # managed" reported the table absent, mergeUpdateDefaults
// appended a second [update] table, and the rewritten file no longer parsed.
func TestTableHeaderSpellings(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   bool
	}{
		{"plain", "[update]", true},
		{"trailing comment", "[update] # managed", true},
		{"inner whitespace", "[ update ]", true},
		{"quoted key", `["update"]`, true},
		{"leading whitespace", "  [update]", true},
		{"trailing tab", "[update]\t", true},
		{"comment with a bracket", "[update] # see [other]", true},

		{"different table", "[audit]", false},
		{"array of tables", "[[update]]", false},
		{"a prefix of the name", "[updates]", false},
		{"a nested table", "[update.nested]", false},
		{"not a header at all", "mode = \"check\"", false},
		{"unterminated", "[update", false},
		{"trailing content", "[update] mode = 1", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := isTopLevelTableHeader(testCase.header, "update"); got != testCase.want {
				t.Fatalf("isTopLevelTableHeader(%q) = %v, want %v",
					testCase.header, got, testCase.want)
			}
		})
	}
}

// TestMergeDoesNotDuplicateADecoratedTable is the consequence the header
// matching exists to prevent: a config whose table the check missed had a
// second copy appended, which TOML rejects as a duplicate key.
func TestMergeDoesNotDuplicateADecoratedTable(t *testing.T) {
	for _, header := range []string{"[update] # managed", "[ update ]", `["update"]`} {
		t.Run(header, func(t *testing.T) {
			contents := header + "\nmode = \"check\"\n"
			merged, err := mergeUpdateDefaults(contents, "")
			if err != nil {
				t.Fatalf("mergeUpdateDefaults: %v", err)
			}
			if strings.Count(merged, "[update]") > 1 {
				t.Fatalf("merged config declares [update] twice:\n%s", merged)
			}
			// The result has to stay loadable, which a duplicate table breaks.
			var decoded Config
			if err := toml.Unmarshal([]byte(merged), &decoded); err != nil {
				t.Fatalf("merged config no longer parses: %v\n%s", err, merged)
			}
		})
	}
}
