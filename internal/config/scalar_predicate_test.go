package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

func predicateRule(t *testing.T, predicate string) *config.Config {
	t.Helper()
	body := `
[[rules]]
name = "predicate"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''anything'''

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
stdout_json_field = "searchable"
stdout_json_equals = ` + predicate + `
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting(%s): %v", predicate, err)
	}
	return cfg
}

// TestScalarPredicateKinds covers the syntax the go-toml migration introduced.
// The predicate is a TOML string, which keeps the field a plain Go string with
// no deferred decode and no empty interface, and the text inside it is read as
// JSON so the compared value stays explicit.
func TestScalarPredicateKinds(t *testing.T) {
	cases := []struct {
		name      string
		predicate string
		wantKind  config.TOMLScalarKind
	}{
		{"boolean", `"true"`, config.TOMLScalarBool},
		{"integer", `"42"`, config.TOMLScalarInt},
		{"float", `"1.5"`, config.TOMLScalarFloat},
		{"json string", `"\"ready\""`, config.TOMLScalarString},
		// A plain word is not valid JSON, so it keeps meaning the obvious
		// thing. This is what stops every existing string predicate breaking.
		{"plain word", `"block"`, config.TOMLScalarString},
		{"word with spaces", `"not ready yet"`, config.TOMLScalarString},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := predicateRule(t, testCase.predicate)
			value := cfg.Rules[0].Conditions[0].StdoutJSONEqualsValue()
			if value.Kind() != testCase.wantKind {
				t.Fatalf("kind = %q, want %q", value.Kind(), testCase.wantKind)
			}
			if !value.IsSet() {
				t.Fatal("value reports unset")
			}
		})
	}
}

// TestPlainWordPredicateKeepsItsText is the compatibility guarantee: an
// existing config saying response_json_equals = "block" still compares against
// the JSON string "block" rather than failing to parse.
func TestPlainWordPredicateKeepsItsText(t *testing.T) {
	cfg := predicateRule(t, `"block"`)
	value := cfg.Rules[0].Conditions[0].StdoutJSONEqualsValue()
	if value.Kind() != config.TOMLScalarString {
		t.Fatalf("kind = %q, want string", value.Kind())
	}
	if got := value.StringValue(); !strings.Contains(got, "block") {
		t.Fatalf("value = %q, want it to carry block", got)
	}
}

// TestUnquotedPredicateIsRejected pins the breaking half of the change. The old
// syntax wrote a bare TOML boolean; that now fails to decode into a string
// field, and the operator has to quote it.
func TestUnquotedPredicateIsRejected(t *testing.T) {
	body := `
[[rules]]
name = "predicate"
events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
pattern = '''anything'''

[[rules.conditions]]
kind = "exec"
command = ["/bin/true"]
stdout_json_field = "searchable"
stdout_json_equals = true
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, err := config.LoadExisting(path); err == nil {
		t.Fatal("an unquoted predicate was accepted; it must be quoted now")
	}
}
