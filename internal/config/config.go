package config

import (
	"fmt"
	"os"
	"regexp"

	"github.com/BurntSushi/toml"
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
	// AuditLog overrides the resolved audit log file path.
	// Empty: use $XDG_STATE_HOME/agent-gate/audit.jsonl (or ~/.local/state/… default).
	AuditLog string `toml:"audit_log"`
}

// Condition is one clause in a multi-condition rule.
// All conditions in a rule must match for the rule to fire (AND semantics).
type Condition struct {
	FieldPaths []string `toml:"field_paths"`
	// Pattern must match the extracted field value for the condition to pass.
	Pattern string `toml:"pattern"`
	// NotPattern, if set, must NOT match the extracted field value.
	NotPattern string `toml:"not_pattern"`

	compiled    *regexp.Regexp
	compiledNot *regexp.Regexp
}

// CompiledPattern returns the pre-compiled regex for Pattern.
func (c *Condition) CompiledPattern() *regexp.Regexp { return c.compiled }

// CompiledNotPattern returns the pre-compiled regex for NotPattern, or nil if unset.
func (c *Condition) CompiledNotPattern() *regexp.Regexp { return c.compiledNot }

// NewCondition constructs a Condition with pre-compiled regexes.
// Intended for tests and programmatic rule construction.
func NewCondition(fieldPaths []string, pattern, notPattern string) (Condition, error) {
	c := Condition{FieldPaths: fieldPaths, Pattern: pattern, NotPattern: notPattern}
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Condition{}, fmt.Errorf("compile pattern %q: %w", pattern, err)
		}
		c.compiled = re
	}
	if notPattern != "" {
		re, err := regexp.Compile(notPattern)
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
	Name             string      `toml:"name"`
	Description      string      `toml:"description"`
	Events           []string    `toml:"events"`
	Conditions       []Condition `toml:"conditions"`
	// FieldPaths and Pattern are used when Conditions is empty (simple rules).
	FieldPaths       []string `toml:"field_paths"`
	Pattern          string   `toml:"pattern"`
	Action           string   `toml:"action"`
	ViolationMessage string   `toml:"violation_message"`

	compiled *regexp.Regexp
}

// Compiled returns the pre-compiled regex for the top-level Pattern.
// Always non-nil after Load() when Conditions is empty.
func (r *Rule) Compiled() *regexp.Regexp {
	return r.compiled
}

// NewSimpleRule constructs a simple (no conditions) Rule with a pre-compiled
// regex. Intended for tests and programmatic rule construction.
func NewSimpleRule(name, pattern string, compiled *regexp.Regexp, events, fieldPaths []string, action, violationMessage string) Rule {
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
					re, err := regexp.Compile(c.Pattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q condition %d: compile pattern %q: %w", r.Name, j, c.Pattern, err)
					}
					c.compiled = re
				}
				if c.NotPattern != "" {
					re, err := regexp.Compile(c.NotPattern)
					if err != nil {
						return nil, fmt.Errorf("rule %q condition %d: compile not_pattern %q: %w", r.Name, j, c.NotPattern, err)
					}
					c.compiledNot = re
				}
			}
		} else {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return nil, fmt.Errorf("rule %q: compile pattern %q: %w", r.Name, r.Pattern, err)
			}
			r.compiled = re
		}
	}

	return &cfg, nil
}
