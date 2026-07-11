package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"goodkind.io/agent-gate/internal/hotkv"
	"goodkind.io/agent-gate/internal/regex"
)

const (
	defaultHookMinimumHotConcurrency = 4
	defaultHookHotConcurrencyFactor  = 4
	defaultHookHotQueueWait          = 25 * time.Millisecond
	defaultHookInferencePhaseTimeout = 4 * time.Second
	defaultHookDeferredQueueLimit    = 8192
	defaultHookDeferredWorkers       = 1
	defaultUpdateInterval            = 24 * time.Hour
	maxHookInferencePhaseTimeoutMS   = 4000
)

// Log holds logging configuration decoded from the [log] TOML table.
type Log struct {
	// Level is one of: debug, info, warn, error.
	Level string `toml:"level"`
}

// Audit holds mature audit event pipeline settings. Pointer bools allow the
// config loader to distinguish "unset" from an explicit false. Audit decisions
// are persisted to SQLite only; the operational agent-gate.jsonl log is for
// debugging agent-gate itself, not audit output.
type Audit struct {
	Enabled *bool       `toml:"enabled"`
	Level   string      `toml:"level"`
	Outputs AuditOutput `toml:"outputs"`
}

// AuditOutput selects audit destinations. SQLite is the sole sink.
type AuditOutput struct {
	SQLite AuditSQLiteOutput `toml:"sqlite"`
}

// AuditSQLiteOutput configures the SQLite audit sink.
type AuditSQLiteOutput struct {
	Path string `toml:"path"`
}

// Performance holds optional tuning for latency-sensitive paths.
type Performance struct {
	Hook HookPerformance `toml:"hook"`
}

// HookPerformance tunes the daemon-owned hook evaluation pipeline.
type HookPerformance struct {
	HotConcurrency          int                  `toml:"hot_concurrency"`
	HotQueueWaitMS          int                  `toml:"hot_queue_wait_ms"`
	InferencePhaseTimeoutMS int                  `toml:"inference_phase_timeout_ms"`
	DeferredQueueLimit      int                  `toml:"deferred_queue_limit"`
	DeferredWorkers         int                  `toml:"deferred_workers"`
	Cache                   HookCachePerformance `toml:"cache"`
}

// HookCachePerformance tunes the daemon-owned hot memory cache used by hook
// evaluation. Entries are process-local and are never persisted to SQLite.
type HookCachePerformance struct {
	MaxEntries      int `toml:"max_entries"`
	MaxValueBytes   int `toml:"max_value_bytes"`
	PruneIntervalMS int `toml:"prune_interval_ms"`
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

	// FieldPair is a comma-separated pair of dot-paths used by the diff condition
	// kind, for example "tool_input.old_string,tool_input.new_string". The first
	// element is the "old" view, the second is the "new" view.
	FieldPair string `toml:"field_pair"`
	// Globs is a list of file-path glob patterns used by the shell-write
	// condition kind. Each parsed write target is matched against this list.
	// The match uses [path/filepath.Match] semantics.
	Globs []string `toml:"globs"`

	// Shell-read-secret condition fields. Pattern matches file contents,
	// PathPattern matches risky paths when content probing is not possible,
	// and ReadSpecs describes how command argv shapes expose paths.
	PathPattern  string           `toml:"path_pattern"`
	MaxBytes     int              `toml:"max_bytes"`
	RemotePolicy string           `toml:"remote_policy"`
	ReadSpecs    []ShellReadSpec  `toml:"read_specs"`
	WriteSpecs   []ShellWriteSpec `toml:"write_specs"`

	// Composer condition fields. RuleSetID selects the lm-review rule set and
	// deterministic oracle pair that decides this gate after cheap prefilters.
	RuleSetID string `toml:"rule_set_id"`

	// Exec condition fields. Command is the argv executed synchronously as an
	// external validator (no shell). TimeoutMs bounds the run. CacheKey is a
	// field selector whose canonicalized value keys the cross-event result
	// cache, held for CacheTTLMs. BlockOn selects which exit codes block, and
	// OnError selects the gate behavior when the validator errors.
	// SearchTools declares the argv0 values this rule treats as code-content
	// searchers when computing read targets (for example "grep", "rg",
	// "git grep", "sed"). The tool set is rule policy with no built-in
	// default: read targets are empty without it, and a cmd_read_targets
	// cache_key requires it.
	Command          []string        `toml:"command"`
	TimeoutMs        int             `toml:"timeout_ms"`
	CacheKey         string          `toml:"cache_key"`
	CacheTTLMs       int             `toml:"cache_ttl_ms"`
	BlockOn          string          `toml:"block_on"`
	OnError          string          `toml:"on_error"`
	SearchTools      []string        `toml:"search_tools"`
	ForEach          string          `toml:"for_each"`
	MatchMode        string          `toml:"match_mode"`
	StdoutJSONField  string          `toml:"stdout_json_field"`
	StdoutJSONEquals *toml.Primitive `toml:"stdout_json_equals"`

	// Infer condition fields. Inline and file declarations are resolved during
	// config compilation, so runtime evaluation never reads configuration files.
	Endpoint               string          `toml:"endpoint"`
	LayerName              string          `toml:"layer_name"`
	Prompt                 string          `toml:"prompt"`
	PromptFile             string          `toml:"prompt_file"`
	InputField             string          `toml:"input_field"`
	OutputSchema           string          `toml:"output_schema"`
	OutputSchemaFile       string          `toml:"output_schema_file"`
	Model                  string          `toml:"model"`
	ReasoningEffort        ReasoningEffort `toml:"reasoning_effort"`
	MaxCompletionTokens    *int64          `toml:"max_completion_tokens"`
	Temperature            *float64        `toml:"temperature"`
	ResponseJSONField      string          `toml:"response_json_field"`
	ResponseJSONEquals     *toml.Primitive `toml:"response_json_equals"`
	ContextSource          string          `toml:"context_source"`
	ContextEndpoint        string          `toml:"context_endpoint"`
	ContextWorkspaceField  string          `toml:"context_workspace_field"`
	ContextSessionField    string          `toml:"context_session_field"`
	ContextTurnBudget      int             `toml:"context_turn_budget"`
	ContextMaxCharsPerTurn int             `toml:"context_max_chars_per_turn"`
	ContextOnError         string          `toml:"context_on_error"`

	compiled                 *regex.Regexp
	compiledNot              *regex.Regexp
	compiledPath             *regex.Regexp
	selectors                []FieldSelectorSpec
	fieldPairs               []FieldPairSpec
	cacheKeySelector         FieldSelectorSpec
	forEachSelector          FieldSelectorSpec
	stdoutJSONValue          TOMLScalarValue
	inputSelector            FieldSelectorSpec
	contextWorkspaceSelector FieldSelectorSpec
	contextSessionSelector   FieldSelectorSpec
	responseJSONValue        TOMLScalarValue
}

// ShellReadSpec describes one configurable shell command shape for the
// shell_read_secret condition kind.
type ShellReadSpec struct {
	Name                        string   `toml:"name"`
	Argv0                       []string `toml:"argv0"`
	PathArgStart                int      `toml:"path_arg_start"`
	PathArgStartIfFlags         []string `toml:"path_arg_start_if_flags"`
	PathArgStartIfFlagsValue    int      `toml:"path_arg_start_if_flags_value"`
	SkipPositionals             int      `toml:"skip_positionals"`
	SkipFlagsWithValues         []string `toml:"skip_flags_with_values"`
	SkipFlagValuesAsPositionals []string `toml:"skip_flag_values_as_positionals"`
	NestedCommand               bool     `toml:"nested_command"`
	NestedCommandFlag           string   `toml:"nested_command_flag"`
	NestedRemote                bool     `toml:"nested_remote"`
	RemoteSources               bool     `toml:"remote_sources"`
}

// FieldPairSpec carries the compiled selectors for a [Condition.FieldPair].
// Old and New are the two sides of the comparison.
type FieldPairSpec struct {
	OldPath  string
	NewPath  string
	OldField FieldSelector
	NewField FieldSelector
}

// FieldPairs returns the parsed [FieldPairSpec] values for a condition.
// The slice has length zero when [Condition.FieldPair] is unset.
func (c *Condition) FieldPairs() []FieldPairSpec { return c.fieldPairs }

// Exec condition block_on variants decide which exit codes block.
const (
	BlockOnNonzero = "nonzero"
	BlockOnZero    = "zero"
	BlockOnMatch   = "match"
)

// Exec condition on_error variants decide what an error (timeout, spawn
// failure, or signal) does to the gate.
const (
	OnErrorOpen   = "open"
	OnErrorClosed = "closed"
)

// Exec match_mode variants decide how many expanded exec runs must match.
const (
	ExecMatchAny = "any"
	ExecMatchAll = "all"
)

// Exec condition defaults and bounds. The timeout is capped well below the 5s
// hook client deadline at internal/daemon/client.go so a slow validator cannot
// stall the hook past its own timeout.
const (
	DefaultExecTimeoutMs = 1500
	MaxExecTimeoutMs     = 4000
	DefaultExecCacheKey  = "effective_cwd"
)

// CompiledPattern returns the pre-compiled regex for Pattern.
func (c *Condition) CompiledPattern() *regex.Regexp { return c.compiled }

// CompiledNotPattern returns the pre-compiled regex for NotPattern, or nil if unset.
func (c *Condition) CompiledNotPattern() *regex.Regexp { return c.compiledNot }

// CompiledPathPattern returns the pre-compiled path regex for shell-read-secret
// conditions, or nil if unset.
func (c *Condition) CompiledPathPattern() *regex.Regexp { return c.compiledPath }

// Selectors returns the compiled [FieldSelectorSpec] list for the condition.
func (c *Condition) Selectors() []FieldSelectorSpec { return c.selectors }

// NewCondition constructs a Condition with pre-compiled regexes.
// Intended for tests and programmatic rule construction.
func NewCondition(fieldPaths []string, pattern, notPattern string) (Condition, error) {
	log := slog.Default()
	var c Condition
	c.Kind = "regex"
	c.FieldPaths = fieldPaths
	c.Pattern = pattern
	c.NotPattern = notPattern
	c.DiagnosticGroup = 0
	c.selectors = CompileFieldSelectorSpecs(fieldPaths)
	c.cacheKeySelector = FieldSelectorSpec{Path: "", Selector: FieldSelectorInvalid}
	c.forEachSelector = FieldSelectorSpec{Path: "", Selector: FieldSelectorInvalid}
	var zeroScalar TOMLScalarValue
	c.stdoutJSONValue = zeroScalar
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

// ParseFieldPair splits a comma-separated old,new dotted-path pair and returns
// a [FieldPairSpec] with both sides compiled. An empty input returns the zero
// value with no error so callers can use this for optional fields.
func ParseFieldPair(spec string) (FieldPairSpec, error) {
	trimmed := strings.TrimSpace(spec)
	if trimmed == "" {
		return FieldPairSpec{OldPath: "", NewPath: "", OldField: FieldSelectorInvalid, NewField: FieldSelectorInvalid}, nil
	}
	parts := strings.Split(trimmed, ",")
	if len(parts) != 2 {
		return FieldPairSpec{OldPath: "", NewPath: "", OldField: FieldSelectorInvalid, NewField: FieldSelectorInvalid},
			fmt.Errorf("field_pair %q: expected exactly two comma-separated paths", spec)
	}
	oldPath := strings.TrimSpace(parts[0])
	newPath := strings.TrimSpace(parts[1])
	return FieldPairSpec{
		OldPath:  oldPath,
		NewPath:  newPath,
		OldField: CompileFieldSelector(oldPath),
		NewField: CompileFieldSelector(newPath),
	}, nil
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
	FieldPaths []string `toml:"field_paths"`
	Pattern    string   `toml:"pattern"`
	NotPattern string   `toml:"not_pattern"`
	// Action selects the rule's effect when it matches. Valid values are
	// "block" (default) and "audit". The legacy "class" field is no longer
	// accepted; its old "sync" maps to action="block" and "deferred" maps to
	// action="audit". A rule with action="audit" never blocks; it logs to the
	// audit layer. Note that the daemon may still emit a config-load WARN if
	// a rule with action="block" subscribes to a (provider, event) pair that
	// the protocol cannot block at; see internal/hook/capability.go.
	Action           string `toml:"action"`
	ViolationMessage string `toml:"violation_message"`
	// DiagnosticGroup selects which capture group supplies diagnostic spans.
	// Zero means the full match.
	DiagnosticGroup int `toml:"diagnostic_group"`
	// DiagnosticFormat selects how blocking diagnostics are rendered.
	// Empty and "detailed" keep the standard rule and match diagnostics.
	DiagnosticFormat string `toml:"diagnostic_format"`
	// RedactDiagnostics hides matched source text in block messages and audit
	// logs for rules that inspect secret-bearing fields.
	RedactDiagnostics bool `toml:"redact_diagnostics"`
	// AuditOnly is set internally from Action during compileRule. Callers
	// should treat this as read-only; it is not a separate user-facing field.
	AuditOnly bool `toml:"-"`
	// DisableIfEnv lists environment variable keys. When the daemon has any of
	// these keys set to a non-empty value in the hook event's environment
	// fingerprint, the rule is skipped without evaluation. Used as a per-rule
	// opt-out (for example a maintenance refresh of a baseline file).
	DisableIfEnv []string `toml:"disable_if_env"`

	compiled    *regex.Regexp
	compiledNot *regex.Regexp
	selectors   []FieldSelectorSpec
}

// Rule action values.
const (
	// ActionBlock is the default. The rule's match stops the tool call when
	// the subscribed (provider, event) pair supports blocking. On post-event
	// hooks that cannot block, the daemon emits a config-load WARN and
	// effectively treats the rule as if it were action="audit".
	ActionBlock = "block"
	// ActionAudit logs the violation to the audit layer without blocking.
	ActionAudit = "audit"
)

// Rule diagnostic format values.
const (
	// DiagnosticFormatDetailed is the default line, column, match, and rule
	// diagnostic format.
	DiagnosticFormatDetailed = "detailed"
	// DiagnosticFormatMessageOnly renders only the configured violation message.
	DiagnosticFormatMessageOnly = "message_only"
)

// Compiled returns the pre-compiled regex for the top-level Pattern.
// Always non-nil after Load() when Conditions is empty.
func (r *Rule) Compiled() *regex.Regexp {
	return r.compiled
}

// CompiledNot returns the pre-compiled regex for the top-level NotPattern, or
// nil when the rule does not define a top-level allow pattern.
func (r *Rule) CompiledNot() *regex.Regexp {
	return r.compiledNot
}

// Selectors returns the compiled [FieldSelectorSpec] list for the rule.
func (r *Rule) Selectors() []FieldSelectorSpec { return r.selectors }

// NewSimpleRule constructs a simple (no conditions) Rule with a pre-compiled
// regex. Intended for tests and programmatic rule construction.
func NewSimpleRule(name, pattern string, compiled *regex.Regexp, events, fieldPaths []string, action, violationMessage string) Rule {
	return Rule{
		Name:              name,
		Description:       "",
		Pattern:           pattern,
		NotPattern:        "",
		Events:            events,
		ClaudeEvents:      nil,
		CursorEvents:      nil,
		CodexEvents:       nil,
		GeminiEvents:      nil,
		Conditions:        nil,
		FieldPaths:        fieldPaths,
		Action:            action,
		ViolationMessage:  violationMessage,
		DiagnosticGroup:   0,
		DiagnosticFormat:  DiagnosticFormatDetailed,
		RedactDiagnostics: false,
		AuditOnly:         false,
		DisableIfEnv:      nil,
		compiled:          compiled,
		compiledNot:       nil,
		selectors:         CompileFieldSelectorSpecs(fieldPaths),
	}
}

// TelemetryConfig holds OTel export settings decoded from the [telemetry]
// TOML table.
type TelemetryConfig struct {
	OTLPEndpoint      string `toml:"otlp_endpoint"`
	SlowOpThresholdMs int    `toml:"slow_op_threshold_ms"`
}

// Config is the top-level configuration structure.
type Config struct {
	Log         Log             `toml:"log"`
	Audit       Audit           `toml:"audit"`
	Paths       Paths           `toml:"paths"`
	Performance Performance     `toml:"performance"`
	Judge       Judge           `toml:"judge"`
	Telemetry   TelemetryConfig `toml:"telemetry"`
	Update      Update          `toml:"update"`
	Rules       []Rule          `toml:"rules"`

	sourceIdentity string
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

// AuditSQLitePath returns the resolved SQLite database path.
func (c *Config) AuditSQLitePath() string {
	if c != nil && c.Audit.Outputs.SQLite.Path != "" {
		return c.Audit.Outputs.SQLite.Path
	}
	return DefaultAuditSQLitePath()
}

// HookHotConcurrency returns the daemon admission limit for synchronous hook
// evaluation.
func (c *Config) HookHotConcurrency() int {
	if c != nil && c.Performance.Hook.HotConcurrency > 0 {
		return c.Performance.Hook.HotConcurrency
	}
	limit := runtime.GOMAXPROCS(0) * defaultHookHotConcurrencyFactor
	if limit < defaultHookMinimumHotConcurrency {
		return defaultHookMinimumHotConcurrency
	}
	return limit
}

// HookHotQueueWait returns the maximum time a hook waits for a hot-path slot.
func (c *Config) HookHotQueueWait() time.Duration {
	if c != nil && c.Performance.Hook.HotQueueWaitMS > 0 {
		return time.Duration(c.Performance.Hook.HotQueueWaitMS) * time.Millisecond
	}
	return defaultHookHotQueueWait
}

// HookInferencePhaseTimeout returns the shared deadline for infer-bearing hot rules.
func (c *Config) HookInferencePhaseTimeout() time.Duration {
	if c != nil && c.Performance.Hook.InferencePhaseTimeoutMS > 0 {
		milliseconds := min(
			c.Performance.Hook.InferencePhaseTimeoutMS,
			maxHookInferencePhaseTimeoutMS,
		)
		return time.Duration(milliseconds) * time.Millisecond
	}
	return defaultHookInferencePhaseTimeout
}

// HookDeferredQueueLimit returns the bounded queue size for cool audit work.
func (c *Config) HookDeferredQueueLimit() int {
	if c != nil && c.Performance.Hook.DeferredQueueLimit > 0 {
		return c.Performance.Hook.DeferredQueueLimit
	}
	return defaultHookDeferredQueueLimit
}

// HookDeferredWorkers returns the number of workers that process cool audit work.
func (c *Config) HookDeferredWorkers() int {
	if c != nil && c.Performance.Hook.DeferredWorkers > 0 {
		return c.Performance.Hook.DeferredWorkers
	}
	return defaultHookDeferredWorkers
}

// HookCacheMaxEntries returns the maximum daemon hot cache entry count.
func (c *Config) HookCacheMaxEntries() int {
	if c != nil && c.Performance.Hook.Cache.MaxEntries > 0 {
		return c.Performance.Hook.Cache.MaxEntries
	}
	return hotkv.DefaultMaxEntries
}

// HookCacheMaxValueBytes returns the maximum bytes accepted per hot cache value.
func (c *Config) HookCacheMaxValueBytes() int {
	if c != nil && c.Performance.Hook.Cache.MaxValueBytes > 0 {
		return c.Performance.Hook.Cache.MaxValueBytes
	}
	return hotkv.DefaultMaxValueBytes
}

// HookCachePruneInterval returns the daemon hot cache periodic prune interval.
func (c *Config) HookCachePruneInterval() time.Duration {
	if c != nil && c.Performance.Hook.Cache.PruneIntervalMS > 0 {
		return time.Duration(c.Performance.Hook.Cache.PruneIntervalMS) * time.Millisecond
	}
	return hotkv.DefaultPruneInterval
}

// Load reads the config file at the XDG config path.
// If no file exists, it returns a zero-value config with default paths.
// All rule patterns are compiled to regexes before returning.
func Load() (*Config, error) {
	return loadPath(Path(), false)
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

	sourceBytes, err := os.ReadFile(path)
	if err != nil {
		log.Error("read config failed", "path", path, "err", err)
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg.sourceIdentity = hashIdentity(sourceBytes)
	meta, err := toml.Decode(string(sourceBytes), &cfg)
	if err != nil {
		log.Error("decode config failed", "path", path, "err", err)
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}
	if err := validateHookPerformance(cfg.Performance.Hook); err != nil {
		return nil, err
	}

	for i := range cfg.Rules {
		if err := compileRule(log, &cfg.Rules[i], meta, filepath.Dir(path)); err != nil {
			return nil, err
		}
	}
	if err := normalizeUpdate(&cfg.Update); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func validateHookPerformance(performance HookPerformance) error {
	if performance.InferencePhaseTimeoutMS < 0 {
		return errors.New("performance.hook.inference_phase_timeout_ms must be non-negative")
	}
	if performance.InferencePhaseTimeoutMS > maxHookInferencePhaseTimeoutMS {
		return fmt.Errorf(
			"performance.hook.inference_phase_timeout_ms must not exceed %d",
			maxHookInferencePhaseTimeoutMS,
		)
	}
	return nil
}

// compileRule attaches compiled regexes and selectors to rule and its
// conditions. Errors are returned with rule-context wrapping.
func compileRule(log *slog.Logger, r *Rule, meta toml.MetaData, configDirectory string) error {
	if err := normalizeRuleAction(r); err != nil {
		return fmt.Errorf("rule %q: %w", r.Name, err)
	}
	if err := normalizeRuleDiagnosticFormat(r); err != nil {
		return fmt.Errorf("rule %q: %w", r.Name, err)
	}
	if len(r.Conditions) > 0 {
		for j := range r.Conditions {
			if err := compileCondition(log, r.Name, j, &r.Conditions[j], meta, configDirectory); err != nil {
				return err
			}
		}
		return nil
	}
	r.selectors = CompileFieldSelectorSpecs(r.FieldPaths)
	re, err := regex.Compile(r.Pattern)
	if err != nil {
		log.Error("compile rule pattern failed", "rule", r.Name, "pattern", r.Pattern, "err", err)
		return fmt.Errorf("rule %q: compile pattern %q: %w", r.Name, r.Pattern, err)
	}
	r.compiled = re
	if r.NotPattern != "" {
		notRe, notErr := regex.Compile(r.NotPattern)
		if notErr != nil {
			log.Error("compile rule not_pattern failed", "rule", r.Name, "not_pattern", r.NotPattern, "err", notErr)
			return fmt.Errorf("rule %q: compile not_pattern %q: %w", r.Name, r.NotPattern, notErr)
		}
		r.compiledNot = notRe
	}
	return validateDiagnosticGroup(fmt.Sprintf("rule %q", r.Name), r.DiagnosticGroup, r.compiled)
}

// normalizeRuleAction validates the Action field and derives AuditOnly from
// it. The action field replaces the legacy class field; the previous "sync"
// maps to ActionBlock and "deferred" maps to ActionAudit. AuditOnly is no
// longer a TOML-readable field; it is set here from Action.
func normalizeRuleAction(r *Rule) error {
	if r.Action == "" {
		r.Action = ActionBlock
	}
	switch r.Action {
	case ActionBlock:
		r.AuditOnly = false
	case ActionAudit:
		r.AuditOnly = true
	default:
		return fmt.Errorf("unknown action %q (expected %q or %q)", r.Action, ActionBlock, ActionAudit)
	}
	return nil
}

func normalizeRuleDiagnosticFormat(r *Rule) error {
	if r.DiagnosticFormat == "" {
		r.DiagnosticFormat = DiagnosticFormatDetailed
	}
	switch r.DiagnosticFormat {
	case DiagnosticFormatDetailed, DiagnosticFormatMessageOnly:
		return nil
	default:
		return fmt.Errorf(
			"unknown diagnostic_format %q (expected %q or %q)",
			r.DiagnosticFormat,
			DiagnosticFormatDetailed,
			DiagnosticFormatMessageOnly,
		)
	}
}

// compileCondition fills in compiled regex and selector state for one
// condition, validates the kind, and parses any field_pair value.
func compileCondition(log *slog.Logger, ruleName string, index int, c *Condition, meta toml.MetaData, configDirectory string) error {
	c.selectors = CompileFieldSelectorSpecs(c.FieldPaths)
	if c.Kind == "" {
		c.Kind = "regex"
	}
	switch ConditionKind(c.Kind) {
	case ConditionKindRegex, ConditionKindCommand, ConditionKindProject, ConditionKindDiff,
		ConditionKindShellRead, ConditionKindShellWrite, ConditionKindExec,
		ConditionKindComposer, ConditionKindGitDefaultBranch,
		ConditionKindGitPrimaryCheckout, ConditionKindGitRefMove, ConditionKindInfer:
	default:
		return fmt.Errorf("rule %q condition %d: unknown kind %q", ruleName, index, c.Kind)
	}
	if c.Pattern != "" {
		re, err := regex.Compile(c.Pattern)
		if err != nil {
			log.Error("compile rule condition pattern failed", "rule", ruleName, "condition_index", index, "pattern", c.Pattern, "err", err)
			return fmt.Errorf("rule %q condition %d: compile pattern %q: %w", ruleName, index, c.Pattern, err)
		}
		c.compiled = re
	}
	if err := validateDiagnosticGroup(fmt.Sprintf("rule %q condition %d", ruleName, index), c.DiagnosticGroup, c.compiled); err != nil {
		return err
	}
	if c.NotPattern != "" {
		re, err := regex.Compile(c.NotPattern)
		if err != nil {
			log.Error("compile rule condition not_pattern failed", "rule", ruleName, "condition_index", index, "not_pattern", c.NotPattern, "err", err)
			return fmt.Errorf("rule %q condition %d: compile not_pattern %q: %w", ruleName, index, c.NotPattern, err)
		}
		c.compiledNot = re
	}
	if c.PathPattern != "" {
		re, err := regex.Compile(c.PathPattern)
		if err != nil {
			log.Error("compile rule condition path_pattern failed", "rule", ruleName, "condition_index", index, "path_pattern", c.PathPattern, "err", err)
			return fmt.Errorf("rule %q condition %d: compile path_pattern %q: %w", ruleName, index, c.PathPattern, err)
		}
		c.compiledPath = re
	}
	if c.FieldPair != "" {
		pair, err := ParseFieldPair(c.FieldPair)
		if err != nil {
			return fmt.Errorf("rule %q condition %d: %w", ruleName, index, err)
		}
		c.fieldPairs = []FieldPairSpec{pair}
	}
	if err := validateShellReadSpecConfig(ruleName, index, c); err != nil {
		return err
	}
	if err := validateComposerConfig(ruleName, index, c); err != nil {
		return err
	}
	if err := compileExecConfig(ruleName, index, c, meta); err != nil {
		return err
	}
	if err := compileInferConfig(log, ruleName, index, c, meta, configDirectory); err != nil {
		return err
	}
	if err := validateShellWriteSpecConfig(ruleName, index, c); err != nil {
		return err
	}
	if ConditionKind(c.Kind) == ConditionKindGitRefMove && len(c.FieldPaths) > 0 {
		return fmt.Errorf("rule %q condition %d: git_ref_move does not accept field_paths", ruleName, index)
	}
	return nil
}

func validateComposerConfig(ruleName string, index int, c *Condition) error {
	if ConditionKind(c.Kind) != ConditionKindComposer {
		return nil
	}
	if strings.TrimSpace(c.RuleSetID) == "" {
		return fmt.Errorf("rule %q condition %d: composer requires rule_set_id", ruleName, index)
	}
	c.RuleSetID = strings.TrimSpace(c.RuleSetID)
	return nil
}

// compileExecConfig validates the exec condition fields, applies defaults, and
// compiles the cache-key field selector. Non-exec conditions are left
// untouched.
func compileExecConfig(ruleName string, index int, c *Condition, meta toml.MetaData) error {
	if ConditionKind(c.Kind) != ConditionKindExec {
		return nil
	}
	if len(c.Command) == 0 || strings.TrimSpace(c.Command[0]) == "" {
		return fmt.Errorf("rule %q condition %d: exec requires a non-empty command", ruleName, index)
	}
	if c.TimeoutMs == 0 {
		c.TimeoutMs = DefaultExecTimeoutMs
	}
	if c.TimeoutMs < 0 {
		return fmt.Errorf("rule %q condition %d: timeout_ms must be non-negative", ruleName, index)
	}
	if c.TimeoutMs > MaxExecTimeoutMs {
		return fmt.Errorf("rule %q condition %d: timeout_ms %d exceeds max %d", ruleName, index, c.TimeoutMs, MaxExecTimeoutMs)
	}
	if c.CacheTTLMs < 0 {
		return fmt.Errorf("rule %q condition %d: cache_ttl_ms must be non-negative", ruleName, index)
	}
	if c.OnError == "" {
		c.OnError = OnErrorOpen
	}
	switch c.OnError {
	case OnErrorOpen, OnErrorClosed:
	default:
		return fmt.Errorf("rule %q condition %d: on_error %q must be %q or %q", ruleName, index, c.OnError, OnErrorOpen, OnErrorClosed)
	}
	if strings.TrimSpace(c.CacheKey) == "" {
		c.CacheKey = DefaultExecCacheKey
	}
	for _, tool := range c.SearchTools {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("rule %q condition %d: search_tools entries must be non-empty", ruleName, index)
		}
	}
	if err := compileExecCacheKey(ruleName, index, c); err != nil {
		return err
	}
	if err := compileExecForEach(ruleName, index, c); err != nil {
		return err
	}
	if err := compileExecStdoutJSON(ruleName, index, c, meta); err != nil {
		return err
	}
	if err := normalizeExecBlockOn(ruleName, index, c); err != nil {
		return err
	}
	return nil
}

func compileExecCacheKey(ruleName string, index int, c *Condition) error {
	specs := CompileFieldSelectorSpecs([]string{c.CacheKey})
	if len(specs) == 0 || specs[0].Selector == FieldSelectorInvalid {
		return fmt.Errorf("rule %q condition %d: cache_key %q is not a valid field selector", ruleName, index, c.CacheKey)
	}
	c.cacheKeySelector = specs[0]
	if c.cacheKeySelector.Selector == FieldCmdReadTargets && len(c.SearchTools) == 0 {
		return fmt.Errorf("rule %q condition %d: cache_key %q requires search_tools to declare which commands count as code search", ruleName, index, c.CacheKey)
	}
	return nil
}

func compileExecForEach(ruleName string, index int, c *Condition) error {
	commandUsesItem := strings.Contains(strings.Join(c.Command, "\x00"), "{{item}}")
	if commandUsesItem && strings.TrimSpace(c.ForEach) == "" {
		return fmt.Errorf("rule %q condition %d: command uses {{item}} but for_each is unset", ruleName, index)
	}
	if strings.TrimSpace(c.ForEach) == "" {
		if c.MatchMode != "" {
			return fmt.Errorf("rule %q condition %d: match_mode requires for_each", ruleName, index)
		}
		return nil
	}

	specs := CompileFieldSelectorSpecs([]string{c.ForEach})
	if len(specs) == 0 || specs[0].Selector == FieldSelectorInvalid {
		return fmt.Errorf("rule %q condition %d: for_each %q is not a valid field selector", ruleName, index, c.ForEach)
	}
	c.forEachSelector = specs[0]
	if !commandUsesItem {
		return fmt.Errorf("rule %q condition %d: for_each %q requires command to contain {{item}}", ruleName, index, c.ForEach)
	}
	if c.MatchMode == "" {
		c.MatchMode = ExecMatchAny
	}
	switch c.MatchMode {
	case ExecMatchAny, ExecMatchAll:
	default:
		return fmt.Errorf("rule %q condition %d: match_mode %q must be %q or %q", ruleName, index, c.MatchMode, ExecMatchAny, ExecMatchAll)
	}
	if c.forEachSelector.Selector == FieldCmdReadTargets && len(c.SearchTools) == 0 {
		return fmt.Errorf("rule %q condition %d: for_each %q requires search_tools to declare which commands count as code search", ruleName, index, c.ForEach)
	}
	return nil
}

func compileExecStdoutJSON(ruleName string, index int, c *Condition, meta toml.MetaData) error {
	c.StdoutJSONField = strings.TrimSpace(c.StdoutJSONField)
	hasJSONField := c.StdoutJSONField != ""
	hasJSONValue := c.StdoutJSONEquals != nil
	if !hasJSONField && !hasJSONValue {
		var zeroScalar TOMLScalarValue
		c.stdoutJSONValue = zeroScalar
		return nil
	}
	if !hasJSONField || !hasJSONValue {
		return fmt.Errorf("rule %q condition %d: stdout_json_field and stdout_json_equals must be set together", ruleName, index)
	}
	if err := validateStdoutJSONFieldPath(c.StdoutJSONField); err != nil {
		return errors.New(
			"rule " + strconv.Quote(ruleName) +
				" condition " + strconv.Itoa(index) +
				": stdout_json_field: " + err.Error(),
		)
	}
	value, err := decodeTOMLScalar(meta, *c.StdoutJSONEquals)
	if err != nil {
		return errors.New(
			"rule " + strconv.Quote(ruleName) +
				" condition " + strconv.Itoa(index) +
				": stdout_json_equals: " + err.Error(),
		)
	}
	c.stdoutJSONValue = value
	return nil
}

func validateStdoutJSONFieldPath(path string) error {
	if path == "" {
		return errors.New("must not be empty")
	}
	if slices.Contains(strings.Split(path, "."), "") {
		return errors.New("must not contain empty path segments")
	}
	return nil
}

func normalizeExecBlockOn(ruleName string, index int, c *Condition) error {
	if c.BlockOn == "" {
		c.BlockOn = BlockOnNonzero
	}
	if c.StdoutJSONField != "" && c.BlockOn == BlockOnNonzero {
		c.BlockOn = BlockOnMatch
	}
	switch c.BlockOn {
	case BlockOnNonzero, BlockOnZero, BlockOnMatch:
	default:
		return fmt.Errorf("rule %q condition %d: block_on %q must be %q, %q, or %q", ruleName, index, c.BlockOn, BlockOnNonzero, BlockOnZero, BlockOnMatch)
	}
	if c.StdoutJSONField != "" && c.BlockOn != BlockOnMatch {
		return fmt.Errorf("rule %q condition %d: stdout_json_field requires block_on = %q", ruleName, index, BlockOnMatch)
	}
	if c.StdoutJSONField == "" && c.BlockOn == BlockOnMatch {
		return fmt.Errorf("rule %q condition %d: block_on %q requires stdout_json_field and stdout_json_equals", ruleName, index, BlockOnMatch)
	}
	return nil
}

func decodeTOMLScalar(meta toml.MetaData, primitive toml.Primitive) (TOMLScalarValue, error) {
	var boolValue bool
	if err := meta.PrimitiveDecode(primitive, &boolValue); err == nil {
		return NewBoolScalar(boolValue), nil
	}
	var stringValue string
	if err := meta.PrimitiveDecode(primitive, &stringValue); err == nil {
		return NewStringScalar(stringValue), nil
	}
	var intValue int64
	if err := meta.PrimitiveDecode(primitive, &intValue); err == nil {
		return NewIntScalar(intValue), nil
	}
	var floatValue float64
	if err := meta.PrimitiveDecode(primitive, &floatValue); err == nil {
		return NewFloatScalar(floatValue), nil
	}
	return TOMLScalarValue{}, fmt.Errorf("expected bool, string, integer, or float")
}

func validateShellReadSpecConfig(ruleName string, index int, c *Condition) error {
	if ConditionKind(c.Kind) != ConditionKindShellRead {
		return nil
	}
	if c.CompiledPattern() == nil {
		return fmt.Errorf("rule %q condition %d: shell_read_secret requires pattern", ruleName, index)
	}
	if c.CompiledPathPattern() == nil {
		return fmt.Errorf("rule %q condition %d: shell_read_secret requires path_pattern", ruleName, index)
	}
	if c.MaxBytes < 0 {
		return fmt.Errorf("rule %q condition %d: max_bytes must be non-negative", ruleName, index)
	}
	if len(c.ReadSpecs) == 0 {
		return fmt.Errorf("rule %q condition %d: shell_read_secret requires at least one read_specs entry", ruleName, index)
	}
	for specIndex := range c.ReadSpecs {
		spec := c.ReadSpecs[specIndex]
		if len(spec.Argv0) == 0 {
			return fmt.Errorf("rule %q condition %d read_specs %d: argv0 is required", ruleName, index, specIndex)
		}
		if spec.PathArgStart < 0 {
			return fmt.Errorf("rule %q condition %d read_specs %d: path_arg_start must be non-negative", ruleName, index, specIndex)
		}
		if spec.PathArgStartIfFlagsValue < 0 {
			return fmt.Errorf("rule %q condition %d read_specs %d: path_arg_start_if_flags_value must be non-negative", ruleName, index, specIndex)
		}
		if spec.SkipPositionals < 0 {
			return fmt.Errorf("rule %q condition %d read_specs %d: skip_positionals must be non-negative", ruleName, index, specIndex)
		}
	}
	return nil
}
