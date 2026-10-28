package config

import (
	"fmt"
	"log/slog"
	"strings"
)

// EnumerationSpec selects the directory arguments of a configured command.
type EnumerationSpec struct {
	Argv0                  []string                  `toml:"argv0"`
	Arguments              ArgumentSpec              `toml:"arguments"`
	TargetMode             string                    `toml:"target_mode"`
	DefaultCWD             bool                      `toml:"default_cwd"`
	RequireContentConsumer bool                      `toml:"require_content_consumer"`
	ConsumerFlags          []string                  `toml:"consumer_flags"`
	ConsumerWrappers       []EnumerationConsumerSpec `toml:"consumer_wrappers"`
}

// EnumerationConsumerSpec selects a command that uses enumerated paths as arguments.
type EnumerationConsumerSpec struct {
	Argv0     []string     `toml:"argv0"`
	Arguments ArgumentSpec `toml:"arguments"`
}

// Enumeration target modes select the directory operands or the command directory.
const (
	EnumerationTargetAllOperands = "all_operands"
	EnumerationTargetCWD         = "cwd"
)

// ValidateEnumerationSpecs checks command and argument declarations before evaluation.
func ValidateEnumerationSpecs(ruleName string, index int, specs []EnumerationSpec) error {
	for specIndex, spec := range specs {
		if !enumerationExecutablesValid(spec.Argv0) {
			return fmt.Errorf("rule %q condition %d enumeration_specs %d: argv0 requires non-empty executable names", ruleName, index, specIndex)
		}
		if spec.TargetMode != EnumerationTargetAllOperands && spec.TargetMode != EnumerationTargetCWD {
			return fmt.Errorf("rule %q condition %d enumeration_specs %d: unknown target_mode %q", ruleName, index, specIndex, spec.TargetMode)
		}
		if err := ValidateArgumentSpec(&spec.Arguments); err != nil {
			slog.Warn("invalid enumeration arguments", "rule", ruleName, "condition", index, "spec", specIndex, "error", err)
			return fmt.Errorf("rule %q condition %d enumeration_specs %d: %w", ruleName, index, specIndex, err)
		}
		for _, flag := range spec.ConsumerFlags {
			if !strings.HasPrefix(flag, "-") {
				return fmt.Errorf("rule %q condition %d enumeration_specs %d: consumer_flags entries must start with '-'", ruleName, index, specIndex)
			}
		}
		for wrapperIndex, wrapper := range spec.ConsumerWrappers {
			if !enumerationExecutablesValid(wrapper.Argv0) {
				return fmt.Errorf("rule %q condition %d enumeration_specs %d consumer_wrappers %d: argv0 requires non-empty executable names", ruleName, index, specIndex, wrapperIndex)
			}
			if err := ValidateArgumentSpec(&wrapper.Arguments); err != nil {
				slog.Warn("invalid enumeration consumer arguments", "rule", ruleName, "condition", index, "spec", specIndex, "wrapper", wrapperIndex, "error", err)
				return fmt.Errorf("rule %q condition %d enumeration_specs %d consumer_wrappers %d: %w", ruleName, index, specIndex, wrapperIndex, err)
			}
		}
	}
	return nil
}

func enumerationExecutablesValid(names []string) bool {
	if len(names) == 0 {
		return false
	}
	for _, name := range names {
		if strings.TrimSpace(name) == "" {
			return false
		}
	}
	return true
}
