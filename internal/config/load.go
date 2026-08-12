package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"

	"goodkind.io/agent-gate/internal/regex"
)

// Load reads the config file at the XDG config path.
// If no file exists, it returns a zero-value config with default paths.
// All rule patterns are compiled to regexes before returning.
func Load() (*Config, error) {
	return loadPath(Path(), false, true)
}

// LoadExisting reads an existing config file and compiles all rule patterns.
func LoadExisting(path string) (*Config, error) {
	return loadPath(path, true, true)
}

// loadPath reads and compiles a config. When strict is true any bad rule or
// settings block fails the whole load, which is what install-time validation
// wants. When strict is false the bad part is dropped or defaulted and recorded
// in Failures, so a running daemon keeps enforcing everything still valid.
func loadPath(path string, requireExisting bool, strict bool) (*Config, error) {
	log := slog.Default()
	var cfg Config

	// recordOrFail returns a non-nil error only in strict mode. In degraded mode
	// it files the failure, runs fallback to put that part back on its defaults,
	// and lets the load carry on without it.
	recordOrFail := func(kind string, scope string, err error, fallback func()) error {
		if err == nil {
			return nil
		}
		if strict {
			return err
		}
		cfg.loadFailures = append(cfg.loadFailures, LoadFailure{
			Scope: scope, Kind: kind, Reason: err.Error(),
		})
		log.Error("config part dropped; enforcement continues without it",
			"kind", kind, "scope", scope, "err", err)
		if fallback != nil {
			fallback()
		}
		return nil
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) && !requireExisting {
			policy, resolveErr := resolveAuditStorage(cfg.Audit.Storage)
			if resolveErr != nil {
				return nil, resolveErr
			}
			cfg.auditStoragePolicy = policy
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
	if err := decodeOrDegrade(log, path, sourceBytes, &cfg, strict); err != nil {
		return nil, err
	}
	// A file that would not decode has no rules and no settings to salvage, so
	// there is nothing further to compile. Returning here rather than walking an
	// empty config keeps the caller's Failures the single description of what
	// happened.
	if cfg.Unusable() {
		cfg.auditStoragePolicy = safeDegradedAuditStoragePolicy()
		return &cfg, nil
	}
	if err := validateSections(log, &cfg, recordOrFail); err != nil {
		return nil, err
	}

	// Install the configured backtracking bounds before any rule pattern
	// compiles, because a PCRE2 handle carries the limits it was built with.
	// This has to happen here rather than at daemon startup: every load,
	// including a reload, compiles its patterns in the loop below, so limits
	// applied after Load returns would never reach a rule.
	regex.SetLimits(cfg.RegexMatchLimit(), cfg.RegexDepthLimit())

	// A rule that will not compile is dropped on its own. Keeping the rest is
	// the difference between losing one rule and losing enforcement entirely.
	declaredRules := len(cfg.Rules)
	kept := cfg.Rules[:0]
	// Held separately from loadFailures, whose first entry may be a settings
	// block recorded earlier by validateSections. Reporting that one would name
	// an unrelated reason for why no rule compiled.
	var firstRuleErr error
	for i := range cfg.Rules {
		compileErr := compileRule(log, &cfg.Rules[i], cfg.Inference, filepath.Dir(path), &cfg)
		if compileErr == nil {
			kept = append(kept, cfg.Rules[i])
			continue
		}
		if firstRuleErr == nil {
			firstRuleErr = compileErr
		}
		if err := recordOrFail(LoadFailureRule, cfg.Rules[i].Name, compileErr, nil); err != nil {
			return nil, err
		}
	}
	cfg.Rules = kept

	// Partial loss is degradation; total loss is a broken file. A config that
	// declared rules and produced none is refused even in degraded mode, so the
	// daemon keeps its previous rule set instead of quietly enforcing nothing.
	// Accepting it would reproduce the outage this whole path exists to prevent,
	// just without the crash to make it visible.
	if !strict && declaredRules > 0 && len(cfg.Rules) == 0 {
		return nil, fmt.Errorf(
			"config %s declared %d rules and none compiled; keeping the previous config: %w",
			path, declaredRules, firstRuleErr,
		)
	}

	if err := recordOrFail(LoadFailureSection, "update",
		normalizeUpdate(&cfg.Update), func() { cfg.Update = zeroUpdate },
	); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// sectionRecorder files one settings-block failure. It returns a non-nil error
// only for a strict load; a degraded load runs fallback and continues.
type sectionRecorder func(kind string, scope string, err error, fallback func()) error

// validateSections checks every settings block. A block that will not validate
// falls back to its defaults on a degraded load rather than killing it. Zeroing
// the block is what makes every accessor return the documented default, so the
// daemon runs on known-good numbers instead of not running at all.
func validateSections(log *slog.Logger, cfg *Config, record sectionRecorder) error {
	storagePolicy, storageErr := resolveAuditStorage(cfg.Audit.Storage)
	if err := record(LoadFailureSection, "audit.storage", storageErr,
		func() { cfg.auditStoragePolicy = safeDegradedAuditStoragePolicy() },
	); err != nil {
		return err
	}
	if storageErr == nil {
		cfg.auditStoragePolicy = storagePolicy
	}
	if err := record(LoadFailureSection, "performance.hook",
		validateHookPerformance(cfg.Performance.Hook, cfg.Performance.Limits),
		func() { cfg.Performance.Hook = zeroHookPerformance },
	); err != nil {
		return err
	}
	if err := record(LoadFailureSection, "performance.timeouts",
		validateTimeouts(cfg.Performance.Timeouts),
		func() { cfg.Performance.Timeouts = zeroTimeoutPerformance },
	); err != nil {
		return err
	}
	if err := record(LoadFailureSection, "performance.limits",
		validateLimits(cfg.Performance.Limits, cfg.Performance.Intervals),
		func() {
			cfg.Performance.Limits = zeroLimitPerformance
			cfg.Performance.Intervals = zeroIntervalPerformance
		},
	); err != nil {
		return err
	}
	// An unusable inference point or judge block leaves the rules that reference
	// it to fail on their own, which the rule loop records separately.
	if err := record(LoadFailureSection, "inference",
		validateInferencePoints(log, cfg.Inference), func() { cfg.Inference = nil },
	); err != nil {
		return err
	}
	return record(LoadFailureSection, "judge",
		validateJudge(cfg.Judge), func() { cfg.Judge = zeroJudge },
	)
}

// Zero values used to put a settings block back on its documented defaults
// after it fails validation. They are declared rather than written as literals
// so adding a field to one of these structs does not need an edit here.
var (
	zeroHookPerformance     HookPerformance
	zeroTimeoutPerformance  TimeoutPerformance
	zeroLimitPerformance    LimitPerformance
	zeroIntervalPerformance IntervalPerformance
	zeroJudge               Judge
	zeroUpdate              Update
)

// decodeOrDegrade decodes the source, or records an unusable config and lets
// the daemon start anyway.
//
// A strict load still refuses the file, which is what install-time validation
// wants. A degraded load does not, because refusing to start preserves no
// enforcement: the daemon exits, every hook finds no daemon, and every call is
// allowed. Measured on 2026-08-05 against a corrupt config, the daemon refused
// to start and a guarded grep was allowed through.
//
// Starting with nothing is the same enforcement as that, and it keeps a process
// alive that can say so. The daemon reports every evaluation as unevaluated
// while it is in this state, so the outage announces itself instead of looking
// like compliance.
func decodeOrDegrade(
	log *slog.Logger,
	path string,
	sourceBytes []byte,
	cfg *Config,
	strict bool,
) error {
	decodeErr := toml.Unmarshal(sourceBytes, cfg)
	if decodeErr == nil {
		return nil
	}
	if strict {
		log.Error("decode config failed", "path", path, "err", decodeErr)
		return fmt.Errorf("decode config %s: %w", path, decodeErr)
	}

	// A partial decode may have left fields set from before the error, and those
	// were never validated. Start from an empty config so nothing half-read
	// reaches enforcement.
	identity := cfg.sourceIdentity
	// Zeroed field by field rather than by literal, so a new config section
	// cannot be silently carried over from a partial decode by being forgotten
	// here.
	var empty Config
	*cfg = empty
	cfg.sourceIdentity = identity
	cfg.loadFailures = []LoadFailure{{
		Scope:  path,
		Kind:   LoadFailureDocument,
		Reason: decodeErr.Error(),
	}}
	log.Error("config did not decode; starting with no rules and reporting "+
		"every call as unevaluated", "path", path, "err", decodeErr)
	return nil
}

// Unusable reports whether the config failed to decode at all, so it carries no
// rules and enforcement is absent rather than merely reduced.
//
// This is distinct from Degraded, which covers a file that decoded and lost
// some part of itself. A caller that must tell "enforcing less" from "enforcing
// nothing" asks this.
func (c *Config) Unusable() bool {
	if c == nil {
		return false
	}
	for _, failure := range c.loadFailures {
		if failure.Kind == LoadFailureDocument {
			return true
		}
	}
	return false
}
