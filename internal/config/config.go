package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"goodkind.io/agent-gate/internal/regex"
)

// Log holds logging configuration decoded from the [log] TOML table.
type Log struct {
	// Level is one of: debug, info, warn, error.
	Level string `toml:"level"`
}

// Paths holds optional explicit path overrides from the [paths] TOML table.
// An empty string for any field means "use the XDG env var (or its default)".
// Non-empty values take highest priority in the resolution chain.
type Paths struct {
	// AuditLog overrides the resolved audit log file path (legacy single-file mode).
	// Empty: use $XDG_STATE_HOME/agent-gate/audit.jsonl (or ~/.local/state/… default).
	AuditLog string `toml:"audit_log"`
	// ClaudeAuditLog overrides the Claude-specific audit log path.
	// Empty: use $XDG_STATE_HOME/agent-gate/audit-claude.jsonl.
	ClaudeAuditLog string `toml:"audit_log_claude"`
	// CursorAuditLog overrides the Cursor-specific audit log path.
	// Empty: use $XDG_STATE_HOME/agent-gate/audit-cursor.jsonl.
	CursorAuditLog string `toml:"audit_log_cursor"`
	// CodexAuditLog overrides the Codex-specific audit log path.
	// Empty: use $XDG_STATE_HOME/agent-gate/audit-codex.jsonl.
	CodexAuditLog string `toml:"audit_log_codex"`
	// GeminiAuditLog overrides the Gemini-specific audit log path.
	// Empty: use $XDG_STATE_HOME/agent-gate/audit-gemini.jsonl.
	GeminiAuditLog string `toml:"audit_log_gemini"`
}

// Condition is one clause in a multi-condition rule.
// All conditions in a rule must match for the rule to fire (AND semantics).
type Condition struct {
	FieldPaths []string `toml:"field_paths"`
	// Pattern must match the extracted field value for the condition to pass.
	Pattern string `toml:"pattern"`
	// NotPattern, if set, must NOT match the extracted field value.
	NotPattern string `toml:"not_pattern"`

	compiled    *regex.Regexp
	compiledNot *regex.Regexp
}

// CompiledPattern returns the pre-compiled regex for Pattern.
func (c *Condition) CompiledPattern() *regex.Regexp { return c.compiled }

// CompiledNotPattern returns the pre-compiled regex for NotPattern, or nil if unset.
func (c *Condition) CompiledNotPattern() *regex.Regexp { return c.compiledNot }

// NewCondition constructs a Condition with pre-compiled regexes.
// Intended for tests and programmatic rule construction.
func NewCondition(fieldPaths []string, pattern, notPattern string) (Condition, error) {
	c := Condition{FieldPaths: fieldPaths, Pattern: pattern, NotPattern: notPattern}
	if pattern != "" {
		re, err := regex.Compile(pattern)
		if err != nil {
			return Condition{}, fmt.Errorf("compile pattern %q: %w", pattern, err)
		}
		c.compiled = re
	}
	if notPattern != "" {
		re, err := regex.Compile(notPattern)
		if err != nil {
			return Condition{}, fmt.Errorf("compile not_pattern %q: %w", notPattern, err)
		}
		c.compiledNot = re
	}
	return c, nil
}

// Rule defines a single enforcement rule decoded from a [[rules]] TOML table.
//
// A rule fires when:
//   - It applies to the current event (Events filter).
//   - AND either:
//     a) Conditions is non-empty and ALL conditions match, OR
//     b) Conditions is empty and the single FieldPaths/Pattern matches.
type Rule struct {
	Name         string      `toml:"name"`
	Description  string      `toml:"description"`
	Events       []string    `toml:"events"`
	ClaudeEvents []string    `toml:"claude_events"`
	CursorEvents []string    `toml:"cursor_events"`
	CodexEvents  []string    `toml:"codex_events"`
	GeminiEvents []string    `toml:"gemini_events"`
	Conditions   []Condition `toml:"conditions"`
	// FieldPaths and Pattern are used when Conditions is empty (simple rules).
	FieldPaths       []string `toml:"field_paths"`
	Pattern          string   `toml:"pattern"`
	Action           string   `toml:"action"`
	ViolationMessage string   `toml:"violation_message"`
	// AuditOnly logs the violation without blocking when true.
	AuditOnly bool `toml:"audit_only"`

	compiled *regex.Regexp
}

// Compiled returns the pre-compiled regex for the top-level Pattern.
// Always non-nil after Load() when Conditions is empty.
func (r *Rule) Compiled() *regex.Regexp {
	return r.compiled
}

// NewSimpleRule constructs a simple (no conditions) Rule with a pre-compiled
// regex. Intended for tests and programmatic rule construction.
func NewSimpleRule(name, pattern string, compiled *regex.Regexp, events, fieldPaths []string, action, violationMessage string) Rule {
	return Rule{
		Name:             name,
		Pattern:          pattern,
		Events:           events,
		FieldPaths:       fieldPaths,
		Action:           action,
		ViolationMessage: violationMessage,
		compiled:         compiled,
	}
}

// Config is the top-level configuration structure.
type Config struct {
	Log   Log    `toml:"log"`
	Paths Paths  `toml:"paths"`
	Rules []Rule `toml:"rules"`
}

// AuditLogPath returns the resolved audit log path applying the full
// override chain: TOML [paths].audit_log > $XDG_STATE_HOME > ~/.local/state.
func (c *Config) AuditLogPath() string {
	if c.Paths.AuditLog != "" {
		return c.Paths.AuditLog
	}
	return DefaultAuditLogPath()
}

// ClaudeAuditLogPath returns the resolved Claude-specific audit log path.
func (c *Config) ClaudeAuditLogPath() string {
	if c.Paths.ClaudeAuditLog != "" {
		return c.Paths.ClaudeAuditLog
	}
	return DefaultClaudeAuditLogPath()
}

// CursorAuditLogPath returns the resolved Cursor-specific audit log path.
func (c *Config) CursorAuditLogPath() string {
	if c.Paths.CursorAuditLog != "" {
		return c.Paths.CursorAuditLog
	}
	return DefaultCursorAuditLogPath()
}

// CodexAuditLogPath returns the resolved Codex-specific audit log path.
func (c *Config) CodexAuditLogPath() string {
	if c.Paths.CodexAuditLog != "" {
		return c.Paths.CodexAuditLog
	}
	return DefaultCodexAuditLogPath()
}

// GeminiAuditLogPath returns the resolved Gemini-specific audit log path.
func (c *Config) GeminiAuditLogPath() string {
	if c.Paths.GeminiAuditLog != "" {
		return c.Paths.GeminiAuditLog
	}
	return DefaultGeminiAuditLogPath()
}

// Load reads the config file at the XDG config path.
// If no file exists, it writes a default config and loads that.
// All rule patterns are compiled to regexes before returning.
func Load() (*Config, error) {
	path := ConfigPath()

	var cfg Config

	if _, err := os.Stat(path); os.IsNotExist(err) {
		// No config file: return zero-value config (no rules, default paths).
		return &cfg, nil
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}

	for i := range cfg.Rules {
		r := &cfg.Rules[i]

		if len(r.Conditions) > 0 {
			for j := range r.Conditions {
				c := &r.Conditions[j]
				if c.Pattern != "" {
					re, err := regex.Compile(c.Pattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q condition %d: compile pattern %q: %w", r.Name, j, c.Pattern, err)
					}
					c.compiled = re
				}
				if c.NotPattern != "" {
					re, err := regex.Compile(c.NotPattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q condition %d: compile not_pattern %q: %w", r.Name, j, c.NotPattern, err)
					}
					c.compiledNot = re
				}
			}
		} else {
			re, err := regex.Compile(r.Pattern)
			if err != nil {
				return nil, fmt.Errorf("rule %q: compile pattern %q: %w", r.Name, r.Pattern, err)
			}
			r.compiled = re
		}
	}

	return &cfg, nil
}
