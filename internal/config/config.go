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

// Rule defines a single enforcement rule decoded from a [[rules]] TOML table.
type Rule struct {
	Name             string   `toml:"name"`
	Description      string   `toml:"description"`
	Events           []string `toml:"events"`
	FieldPaths       []string `toml:"field_paths"`
	Pattern          string   `toml:"pattern"`
	Action           string   `toml:"action"`
	ViolationMessage string   `toml:"violation_message"`

	// compiled holds the pre-compiled regex for Pattern.
	// It is populated by Load() and is not read from TOML.
	compiled *regexp.Regexp
}

// Compiled returns the pre-compiled regex for this rule.
// It is always non-nil after a successful call to Load() or NewRule().
func (r *Rule) Compiled() *regexp.Regexp {
	return r.compiled
}

// NewRule constructs a Rule with a pre-compiled regex.
// Intended for use in tests and programmatic rule construction where
// the config file loading path is bypassed.
func NewRule(name, pattern string, compiled *regexp.Regexp, events, fieldPaths []string, action, violationMessage string) Rule {
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

	if _, err := os.Stat(path); os.IsNotExist(err) {
		if writeErr := writeDefault(path); writeErr != nil {
			return nil, fmt.Errorf("write default config to %s: %w", path, writeErr)
		}
	}

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}

	for i := range cfg.Rules {
		re, err := regexp.Compile(cfg.Rules[i].Pattern)
		if err != nil {
			return nil, fmt.Errorf("rule %q: compile pattern %q: %w",
				cfg.Rules[i].Name, cfg.Rules[i].Pattern, err)
		}
		cfg.Rules[i].compiled = re
	}

	return &cfg, nil
}
