package config

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/BurntSushi/toml"
	"goodkind.io/agent-gate/internal/regex"
)

// Log holds logging configuration decoded from the [log] TOML table.
type Log struct {
	// Level is one of: debug, info, warn, error.
	Level string `toml:"level"`
}

// Audit holds mature audit event pipeline settings. Pointer bools allow the
// config loader to distinguish "unset" from an explicit false.
type Audit struct {
	Enabled *bool       `toml:"enabled"`
	Level   string      `toml:"level"`
	Outputs AuditOutput `toml:"outputs"`
	Query   AuditQuery  `toml:"query"`
}

// AuditOutput selects which audit destinations are active.
type AuditOutput struct {
	JSONL  AuditJSONLOutput  `toml:"jsonl"`
	SQLite AuditSQLiteOutput `toml:"sqlite"`
}

// AuditJSONLOutput configures the JSONL file audit sink.
type AuditJSONLOutput struct {
	Enabled          *bool  `toml:"enabled"`
	EventsDir        string `toml:"events_dir"`
	PayloadsDir      string `toml:"payloads_dir"`
	WriteRawPayloads *bool  `toml:"write_raw_payloads"`
}

// AuditSQLiteOutput configures the SQLite audit sink.
type AuditSQLiteOutput struct {
	Enabled *bool  `toml:"enabled"`
	Path    string `toml:"path"`
}

// AuditQuery configures the audit query subsystem.
type AuditQuery struct {
	Prefer string `toml:"prefer"`
}

// Paths holds optional explicit path overrides from the [paths] TOML table.
// An empty string for any field means "use the XDG env var (or its default)".
// Non-empty values take highest priority in the resolution chain.
type Paths struct {
	// ConversationsDir overrides the base directory for per-conversation logs.
	// Empty: use $XDG_STATE_HOME/agent-gate/conversations.
	ConversationsDir string `toml:"conversations_dir"`
}

// Condition is one clause in a multi-condition rule.
// All conditions in a rule must match for the rule to fire (AND semantics).
type Condition struct {
	// Kind selects the condition evaluator. Empty means "regex" for backward
	// compatibility with existing configs.
	Kind string `toml:"kind"`

	FieldPaths []string `toml:"field_paths"`
	// Pattern must match the extracted field value for the condition to pass.
	// For command conditions, this is applied to the normalized command tail
	// (subcommand plus arguments).
	Pattern string `toml:"pattern"`
	// NotPattern, if set, must NOT match the extracted field value. For command
	// conditions, this is applied to the same normalized command tail.
	NotPattern string `toml:"not_pattern"`
	// DiagnosticGroup selects which capture group supplies diagnostic spans.
	// Zero means the full match.
	DiagnosticGroup int `toml:"diagnostic_group"`

	// Command condition fields.
	Argv0       string   `toml:"argv0"`
	Subcommands []string `toml:"subcommands"`
	StripEnv    bool     `toml:"strip_env"`
	StripArgs   []string `toml:"strip_args"`
	CwdFlags    []string `toml:"cwd_flags"`

	// Project condition fields. Paths are evaluated relative to the discovered
	// project root. If RootMarkers is set, the root is the nearest ancestor
	// containing any marker.
	RootMarkers []string `toml:"root_markers"`
	RequireAny  []string `toml:"require_any"`
	RequireAll  []string `toml:"require_all"`
	ForbidAny   []string `toml:"forbid_any"`

	compiled    *regex.Regexp
	compiledNot *regex.Regexp
	selectors   []FieldSelectorSpec
}

// CompiledPattern returns the pre-compiled regex for Pattern.
func (c *Condition) CompiledPattern() *regex.Regexp { return c.compiled }

// CompiledNotPattern returns the pre-compiled regex for NotPattern, or nil if unset.
func (c *Condition) CompiledNotPattern() *regex.Regexp { return c.compiledNot }

// Selectors returns the compiled [FieldSelectorSpec] list for the condition.
func (c *Condition) Selectors() []FieldSelectorSpec { return c.selectors }

// NewCondition constructs a Condition with pre-compiled regexes.
// Intended for tests and programmatic rule construction.
func NewCondition(fieldPaths []string, pattern, notPattern string) (Condition, error) {
	log := slog.Default()
	c := Condition{Kind: "regex", FieldPaths: fieldPaths, Pattern: pattern, NotPattern: notPattern, selectors: CompileFieldSelectorSpecs(fieldPaths)}
	if pattern != "" {
		re, err := regex.Compile(pattern)
		if err != nil {
			log.Error("compile condition pattern failed", "pattern", pattern, "err", err)
			return Condition{}, fmt.Errorf("compile pattern %q: %w", pattern, err)
		}
		c.compiled = re
	}
	if notPattern != "" {
		re, err := regex.Compile(notPattern)
		if err != nil {
			log.Error("compile condition not_pattern failed", "not_pattern", notPattern, "err", err)
			return Condition{}, fmt.Errorf("compile not_pattern %q: %w", notPattern, err)
		}
		c.compiledNot = re
	}
	return c, nil
}

func validateDiagnosticGroup(context string, group int, re *regex.Regexp) error {
	if group < 0 {
		return fmt.Errorf("%s: diagnostic_group must be non-negative", context)
	}
	if group == 0 {
		return nil
	}
	if re == nil {
		return fmt.Errorf("%s: diagnostic_group requires a pattern", context)
	}
	if group > int(re.CaptureCount()) {
		return fmt.Errorf("%s: diagnostic_group %d exceeds capture count %d", context, group, re.CaptureCount())
	}
	return nil
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
	// DiagnosticGroup selects which capture group supplies diagnostic spans.
	// Zero means the full match.
	DiagnosticGroup int `toml:"diagnostic_group"`
	// AuditOnly logs the violation without blocking when true.
	AuditOnly bool `toml:"audit_only"`

	compiled  *regex.Regexp
	selectors []FieldSelectorSpec
}

// Compiled returns the pre-compiled regex for the top-level Pattern.
// Always non-nil after Load() when Conditions is empty.
func (r *Rule) Compiled() *regex.Regexp {
	return r.compiled
}

// Selectors returns the compiled [FieldSelectorSpec] list for the rule.
func (r *Rule) Selectors() []FieldSelectorSpec { return r.selectors }

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
		selectors:        CompileFieldSelectorSpecs(fieldPaths),
	}
}

// Config is the top-level configuration structure.
type Config struct {
	Log   Log    `toml:"log"`
	Audit Audit  `toml:"audit"`
	Paths Paths  `toml:"paths"`
	Rules []Rule `toml:"rules"`
}

// ConversationsDir returns the resolved base directory for per-conversation
// audit logs. Each conversation gets its own subfolder.
func (c *Config) ConversationsDir() string {
	if c.Paths.ConversationsDir != "" {
		return c.Paths.ConversationsDir
	}
	return DefaultConversationsDir()
}

// AuditEnabled reports whether audit logging is enabled. Default is true.
func (c *Config) AuditEnabled() bool {
	if c != nil && c.Audit.Enabled != nil {
		return *c.Audit.Enabled
	}
	return true
}

// AuditLevel returns the configured minimum audit log level, falling back
// to the global log level when audit-specific level is unset.
func (c *Config) AuditLevel() string {
	if c != nil && c.Audit.Level != "" {
		return c.Audit.Level
	}
	if c != nil {
		return c.Log.Level
	}
	return ""
}

// AuditJSONLEnabled reports whether the JSONL audit sink is enabled.
func (c *Config) AuditJSONLEnabled() bool {
	if c != nil && c.Audit.Outputs.JSONL.Enabled != nil {
		return *c.Audit.Outputs.JSONL.Enabled
	}
	return true
}

// AuditSQLiteEnabled reports whether the SQLite audit sink is enabled.
func (c *Config) AuditSQLiteEnabled() bool {
	if c != nil && c.Audit.Outputs.SQLite.Enabled != nil {
		return *c.Audit.Outputs.SQLite.Enabled
	}
	return false
}

// AuditWriteRawPayloads reports whether the JSONL sink should persist the
// original raw hook payload as a content-addressed blob.
func (c *Config) AuditWriteRawPayloads() bool {
	if c != nil && c.Audit.Outputs.JSONL.WriteRawPayloads != nil {
		return *c.Audit.Outputs.JSONL.WriteRawPayloads
	}
	return true
}

// AuditEventsDir returns the resolved JSONL audit events directory.
func (c *Config) AuditEventsDir() string {
	if c != nil && c.Audit.Outputs.JSONL.EventsDir != "" {
		return c.Audit.Outputs.JSONL.EventsDir
	}
	return DefaultAuditEventsDir()
}

// AuditPayloadsDir returns the resolved raw-payload directory.
func (c *Config) AuditPayloadsDir() string {
	if c != nil && c.Audit.Outputs.JSONL.PayloadsDir != "" {
		return c.Audit.Outputs.JSONL.PayloadsDir
	}
	return DefaultAuditPayloadsDir()
}

// AuditSQLitePath returns the resolved SQLite database path.
func (c *Config) AuditSQLitePath() string {
	if c != nil && c.Audit.Outputs.SQLite.Path != "" {
		return c.Audit.Outputs.SQLite.Path
	}
	return DefaultAuditSQLitePath()
}

// AuditQueryPrefer returns the configured preferred audit query backend.
func (c *Config) AuditQueryPrefer() string {
	if c != nil && c.Audit.Query.Prefer != "" {
		return c.Audit.Query.Prefer
	}
	return "sqlite"
}

// Load reads the config file at the XDG config path.
// If no file exists, it returns a zero-value config with default paths.
// All rule patterns are compiled to regexes before returning.
func Load() (*Config, error) {
	return loadPath(ConfigPath(), false)
}

// LoadExisting reads an existing config file and compiles all rule patterns.
func LoadExisting(path string) (*Config, error) {
	return loadPath(path, true)
}

func loadPath(path string, requireExisting bool) (*Config, error) {
	log := slog.Default()
	var cfg Config

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) && !requireExisting {
			return &cfg, nil
		}
		log.Error("stat config failed", "path", path, "err", err)
		return nil, fmt.Errorf("stat config %s: %w", path, err)
	}

	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		log.Error("decode config failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}

	for i := range cfg.Rules {
		r := &cfg.Rules[i]

		if len(r.Conditions) > 0 {
			for j := range r.Conditions {
				c := &r.Conditions[j]
				c.selectors = CompileFieldSelectorSpecs(c.FieldPaths)
				if c.Kind == "" {
					c.Kind = "regex"
				}
				switch c.Kind {
				case "regex", "command", "project":
				default:
					return nil, fmt.Errorf("rule %q condition %d: unknown kind %q", r.Name, j, c.Kind)
				}
				if c.Pattern != "" {
					re, err := regex.Compile(c.Pattern)
					if err != nil {
						log.Error("compile rule condition pattern failed", "rule", r.Name, "condition_index", j, "pattern", c.Pattern, "err", err)
						return nil, fmt.Errorf("rule %q condition %d: compile pattern %q: %w", r.Name, j, c.Pattern, err)
					}
					c.compiled = re
				}
				if err := validateDiagnosticGroup(fmt.Sprintf("rule %q condition %d", r.Name, j), c.DiagnosticGroup, c.compiled); err != nil {
					return nil, err
				}
				if c.NotPattern != "" {
					re, err := regex.Compile(c.NotPattern)
					if err != nil {
						log.Error("compile rule condition not_pattern failed", "rule", r.Name, "condition_index", j, "not_pattern", c.NotPattern, "err", err)
						return nil, fmt.Errorf("rule %q condition %d: compile not_pattern %q: %w", r.Name, j, c.NotPattern, err)
					}
					c.compiledNot = re
				}
			}
		} else {
			r.selectors = CompileFieldSelectorSpecs(r.FieldPaths)
			re, err := regex.Compile(r.Pattern)
			if err != nil {
				log.Error("compile rule pattern failed", "rule", r.Name, "pattern", r.Pattern, "err", err)
				return nil, fmt.Errorf("rule %q: compile pattern %q: %w", r.Name, r.Pattern, err)
			}
			r.compiled = re
			if err := validateDiagnosticGroup(fmt.Sprintf("rule %q", r.Name), r.DiagnosticGroup, r.compiled); err != nil {
				return nil, err
			}
		}
	}

	return &cfg, nil
}
