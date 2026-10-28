package config

import (
	_ "embed"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// ProsePolicy selects command text for prose rules.
type ProsePolicy struct {
	PathPatterns []string           `toml:"path_patterns"`
	Commands     []ProseCommandSpec `toml:"commands"`
	Heredocs     bool               `toml:"heredocs"`
}

// ProseCommandSpec selects parsed option values or operands.
type ProseCommandSpec struct {
	Argv0             []string `toml:"argv0"`
	ValueOptions      []string `toml:"value_options"`
	Operands          bool     `toml:"operands"`
	RequireOutputPath bool     `toml:"require_output_path"`
	// Arguments is required. The pointer distinguishes an omitted table from an
	// explicitly empty argument description during validation.
	Arguments *ArgumentSpec `toml:"arguments"`
}

// RedirectionPolicy selects output paths and descriptor duplications to ignore.
type RedirectionPolicy struct {
	IgnorePaths []string `toml:"ignore_paths"`
	IgnoreFDs   []string `toml:"ignore_fds"`
}

//go:embed selector_policy_defaults.toml
var selectorPolicyDefaultsSource []byte

type selectorPolicyDefaults struct {
	Prose       ProsePolicy       `toml:"prose_policy"`
	Redirection RedirectionPolicy `toml:"redirection_policy"`
}

var selectorDefaults = sync.OnceValues(loadSelectorPolicyDefaults)

func loadSelectorPolicyDefaults() (selectorPolicyDefaults, error) {
	var defaults selectorPolicyDefaults
	if err := toml.Unmarshal(selectorPolicyDefaultsSource, &defaults); err != nil {
		slog.Error("decode shipped selector policies failed", "err", err)
		return defaults, fmt.Errorf("decode shipped selector policies: %w", err)
	}
	if err := validateProsePolicy(&defaults.Prose); err != nil {
		slog.Error("validate shipped prose policy failed", "err", err)
		return defaults, fmt.Errorf("validate shipped prose policy: %w", err)
	}
	if err := validateRedirectionPolicy(&defaults.Redirection); err != nil {
		slog.Error("validate shipped redirection policy failed", "err", err)
		return defaults, fmt.Errorf("validate shipped redirection policy: %w", err)
	}
	return defaults, nil
}

// ValidateSelectorPolicyDefaults reports invalid shipped selector configuration.
func ValidateSelectorPolicyDefaults() error {
	_, err := selectorDefaults()
	return err
}

// DefaultProsePolicy returns the shipped configuration for prose extraction.
func DefaultProsePolicy() *ProsePolicy {
	defaults, err := selectorDefaults()
	if err != nil {
		slog.Error("read shipped prose policy failed", "err", err)
		return nil
	}
	return &defaults.Prose
}

// DefaultRedirectionPolicy returns the shipped configuration for redirections.
func DefaultRedirectionPolicy() *RedirectionPolicy {
	defaults, err := selectorDefaults()
	if err != nil {
		slog.Error("read shipped redirection policy failed", "err", err)
		return nil
	}
	return &defaults.Redirection
}

// ValidateSelectorPolicies validates condition-owned prose and redirection policy.
func ValidateSelectorPolicies(ruleName string, index int, prose *ProsePolicy, redirects *RedirectionPolicy) error {
	if err := ValidateSelectorPolicyDefaults(); err != nil {
		return err
	}
	if prose != nil {
		if err := validateProsePolicy(prose); err != nil {
			slog.Error("validate prose policy failed", "rule", ruleName, "condition", index, "err", err)
			return fmt.Errorf("rule %q condition %d prose_policy: %w", ruleName, index, err)
		}
	}
	if redirects != nil {
		if err := validateRedirectionPolicy(redirects); err != nil {
			slog.Error("validate redirection policy failed", "rule", ruleName, "condition", index, "err", err)
			return fmt.Errorf("rule %q condition %d redirection_policy: %w", ruleName, index, err)
		}
	}
	return nil
}

func validateRedirectionPolicy(policy *RedirectionPolicy) error {
	for _, path := range policy.IgnorePaths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("ignore_paths entries must be non-empty")
		}
	}
	for _, descriptor := range policy.IgnoreFDs {
		if descriptor == "" || strings.IndexFunc(descriptor, func(character rune) bool { return character < '0' || character > '9' }) >= 0 {
			return fmt.Errorf("ignore_fds entries must contain decimal digits")
		}
	}
	return nil
}

func validateProsePolicy(policy *ProsePolicy) error {
	for _, pattern := range policy.PathPatterns {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("path_patterns entries must be non-empty")
		}
		if _, err := filepath.Match(pattern, ""); err != nil {
			slog.Error("validate prose path pattern failed", "pattern", pattern, "err", err)
			return fmt.Errorf("invalid path pattern %q: %w", pattern, err)
		}
	}
	for commandIndex, command := range policy.Commands {
		if len(command.Argv0) == 0 || !nonEmptyFlagEntries(command.Argv0) {
			return fmt.Errorf("commands %d: argv0 entries must be non-empty", commandIndex)
		}
		if !command.Operands && len(command.ValueOptions) == 0 {
			return fmt.Errorf("commands %d: operands or value_options is required", commandIndex)
		}
		if !flagEntries(command.ValueOptions) || !nonEmptyFlagEntries(command.ValueOptions) {
			return fmt.Errorf("commands %d: value_options entries must be non-empty options", commandIndex)
		}
		if command.Arguments == nil {
			return fmt.Errorf("commands %d: arguments is required", commandIndex)
		}
		if err := ValidateArgumentSpec(command.Arguments); err != nil {
			slog.Error("validate prose arguments failed", "command", commandIndex, "err", err)
			return fmt.Errorf("commands %d arguments: %w", commandIndex, err)
		}
		for _, name := range command.ValueOptions {
			if !proseOptionHasValue(command.Arguments, name) {
				return fmt.Errorf("commands %d: value option %q requires a declared value-taking option", commandIndex, name)
			}
		}
	}
	return nil
}

func proseOptionHasValue(arguments *ArgumentSpec, name string) bool {
	for _, option := range arguments.Options {
		if slices.Contains(option.Names, name) {
			return option.Value != "none"
		}
	}
	return false
}
