package config

import "fmt"

// LoadFailure records one part of a config that could not be used, so the
// daemon can drop that part alone and still enforce everything else.
//
// Before this existed, config load was all-or-nothing: one rule with a bad
// value failed the whole file, every rule was dropped, and the daemon exited
// rather than starting. Because the hook fails open when no daemon answers,
// that turned a single mistyped number into a total loss of enforcement.
type LoadFailure struct {
	// Scope is the rule name for a rule that would not compile, or the config
	// section name for a settings block that would not validate.
	Scope string
	// Kind is "rule" or "section", so a reader can tell a dropped rule from a
	// settings block that fell back to defaults.
	Kind string
	// Reason is the validation error, kept as text because it is reported to
	// operators and written to a durable record rather than matched on.
	Reason string
}

// String renders one failure for a log line or an operator-facing report.
func (f LoadFailure) String() string {
	return fmt.Sprintf("%s %q: %s", f.Kind, f.Scope, f.Reason)
}

const (
	// LoadFailureRule marks a rule that was dropped on its own.
	LoadFailureRule = "rule"
	// LoadFailureSection marks a settings block that fell back to defaults.
	LoadFailureSection = "section"
)

// Failures returns the parts of the config that were dropped or defaulted
// during a degraded load. A strict load never returns any, because it fails
// instead.
func (c *Config) Failures() []LoadFailure {
	if c == nil {
		return nil
	}
	return c.loadFailures
}

// Degraded reports whether any part of the config was dropped or defaulted, so
// callers can decide to warn without inspecting the list.
func (c *Config) Degraded() bool {
	return len(c.Failures()) > 0
}

// LoadDegraded reads the config at the default path and keeps everything that
// is usable, recording what it had to drop instead of failing the whole file.
//
// The daemon uses this so a single bad rule costs that rule and nothing else.
// Install-time validation still uses Load, which refuses a config with any
// failure, so a mistake is caught before it reaches a running daemon.
func LoadDegraded() (*Config, error) {
	return loadPath(Path(), false, false)
}

// LoadDegradedPath is LoadDegraded against an explicit path, for tests and for
// callers that do not use the default XDG location.
func LoadDegradedPath(path string) (*Config, error) {
	return loadPath(path, true, false)
}
