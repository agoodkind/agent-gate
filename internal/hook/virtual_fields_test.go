package hook_test

import (
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hook"
)

// syntheticSelectors are the field paths the rules engine synthesizes rather
// than reading from a hook payload. None appears in any event schema, so each
// has to be accepted explicitly or every rule naming it is rejected.
var syntheticSelectors = []string{
	"effective_cwd",
	"cmd_segments",
	"cmd_comments",
	"cmd_double_hyphen_prose",
	"cmd_redirections",
	"cmd_write_targets",
	"patch_write_targets",
}

// TestEverySyntheticSelectorValidatesInARule is the regression for a selector
// that resolved but would not validate. patch_write_targets was registered in
// internal/config, so the engine could compute it, and left out of the schema's
// virtual-field list, so ValidateConfig rejected every rule that named it.
//
// The failure is quiet in the worst way: the binary ships the capability, the
// config declares it, and the reload is refused, so the daemon keeps running on
// its previous rules and the new guard silently never applies. That is exactly
// how a Codex apply_patch write stayed invisible after the code fix landed and
// was deployed.
func TestEverySyntheticSelectorValidatesInARule(t *testing.T) {
	for _, selector := range syntheticSelectors {
		t.Run(selector, func(t *testing.T) {
			body := `
[[rules]]
name = "synthetic"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
field_paths = ["` + selector + `"]
pattern = '''anything'''
`
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := config.LoadExisting(path)
			if err != nil {
				t.Fatalf("LoadExisting rejected %q: %v", selector, err)
			}
			if errs := hook.ValidateConfig(cfg); len(errs) > 0 {
				t.Fatalf("ValidateConfig rejected %q: %v\n"+
					"a rule naming it would be refused, and the daemon would keep "+
					"its previous rules while the new guard silently never applies",
					selector, errs[0])
			}
		})
	}
}

// TestAnUnknownSelectorIsStillRejected keeps the allowlist meaningful: a typo
// must still fail loudly rather than pass as a synthetic field.
func TestAnUnknownSelectorIsStillRejected(t *testing.T) {
	body := `
[[rules]]
name = "typo"
claude_events = ["PreToolUse"]
action = "block"
violation_message = "blocked"
field_paths = ["patch_write_target"]
pattern = '''anything'''
`
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		// A load-time rejection is also a loud failure, which is what matters.
		return
	}
	if errs := hook.ValidateConfig(cfg); len(errs) == 0 {
		t.Fatal("a misspelled selector was accepted")
	}
}
