package config

import (
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
)

// ArgumentSpec defines command argument roles without selecting enforcement actions.
type ArgumentSpec struct {
	Subcommands           []string                  `toml:"subcommands"`
	Options               []ArgumentOptionSpec      `toml:"options"`
	ShortClusters         bool                      `toml:"short_clusters"`
	EndOfOptions          bool                      `toml:"end_of_options"`
	StopAtFirstPositional bool                      `toml:"stop_at_first_positional"`
	SkipPositionals       int                       `toml:"skip_positionals"`
	ProgramOptions        []string                  `toml:"program_options"`
	RequireOptions        []string                  `toml:"require_options"`
	RequireOptionValues   []ArgumentOptionPredicate `toml:"require_option_values"`
	ExcludeOptionValues   []ArgumentOptionPredicate `toml:"exclude_option_values"`
	IgnoreOperandPrefixes []string                  `toml:"ignore_operand_prefixes"`
	OperandStopTokens     []string                  `toml:"operand_stop_tokens"`
	OperandsBeforeOptions bool                      `toml:"operands_before_options"`
	UnknownOptions        string                    `toml:"unknown_options"`
}

// ArgumentOptionSpec defines an option's aliases and accepted value syntax.
type ArgumentOptionSpec struct {
	Names                []string `toml:"names"`
	Value                string   `toml:"value"`
	Forms                []string `toml:"forms"`
	CWD                  bool     `toml:"cwd"`
	AttachedValuePattern string   `toml:"attached_value_pattern"`
}

// ArgumentOptionPredicate selects literal values of a parsed option.
type ArgumentOptionPredicate struct {
	Option string   `toml:"option"`
	Values []string `toml:"values"`
}

// ValidateArgumentSpec rejects ambiguous or unsupported argument descriptions.
func ValidateArgumentSpec(spec *ArgumentSpec) error {
	if spec == nil {
		return nil
	}
	if spec.SkipPositionals < 0 {
		return fmt.Errorf("skip_positionals must be non-negative")
	}
	if spec.UnknownOptions != "" && spec.UnknownOptions != "unresolved" && spec.UnknownOptions != "skip" {
		return fmt.Errorf("unknown_options must be unresolved or skip")
	}
	known := make(map[string]ArgumentOptionSpec)
	for index, option := range spec.Options {
		if err := validateArgumentOption(option, known); err != nil {
			slog.Error("invalid argument option", "option_index", index, "err", err)
			return fmt.Errorf("options %d: %w", index, err)
		}
	}
	if err := validateArgumentReferences(spec, known); err != nil {
		return err
	}
	for _, values := range [][]string{spec.Subcommands, spec.IgnoreOperandPrefixes, spec.OperandStopTokens} {
		for _, value := range values {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("argument entries must be non-empty")
			}
		}
	}
	return nil
}

func validateArgumentOption(option ArgumentOptionSpec, known map[string]ArgumentOptionSpec) error {
	if len(option.Names) == 0 {
		return fmt.Errorf("names are required")
	}
	for _, name := range option.Names {
		if !strings.HasPrefix(name, "-") || name == "-" || name == "--" || strings.ContainsAny(name, "= \t\r\n") {
			return fmt.Errorf("invalid option name %q", name)
		}
		if _, exists := known[name]; exists {
			return fmt.Errorf("duplicate option %q", name)
		}
		known[name] = option
	}
	if !slices.Contains([]string{"none", "required", "optional"}, option.Value) {
		return fmt.Errorf("value must be none, required, or optional")
	}
	if option.Value == "none" && (len(option.Forms) != 0 || option.CWD) {
		return fmt.Errorf("a valueless option cannot declare forms or cwd")
	}
	if option.Value != "none" && len(option.Forms) == 0 {
		return fmt.Errorf("value forms are required")
	}
	for _, form := range option.Forms {
		if !slices.Contains([]string{"separate", "attached", "equals"}, form) {
			return fmt.Errorf("unknown value form %q", form)
		}
	}
	if option.AttachedValuePattern != "" {
		if !slices.Contains(option.Forms, "attached") || !strings.HasPrefix(option.AttachedValuePattern, "^") {
			return fmt.Errorf("attached_value_pattern requires attached values and a start anchor")
		}
		if _, err := regexp.Compile(option.AttachedValuePattern); err != nil {
			slog.Error("invalid attached argument pattern", "err", err)
			return fmt.Errorf("attached_value_pattern: %w", err)
		}
	}
	return nil
}

func validateArgumentReferences(spec *ArgumentSpec, known map[string]ArgumentOptionSpec) error {
	for _, names := range [][]string{spec.ProgramOptions, spec.RequireOptions} {
		for _, name := range names {
			if _, exists := known[name]; !exists {
				return fmt.Errorf("option reference %q is undeclared", name)
			}
		}
	}
	for _, name := range spec.ProgramOptions {
		if known[name].Value == "none" {
			return fmt.Errorf("program option %q must accept a value", name)
		}
	}
	for _, predicates := range [][]ArgumentOptionPredicate{spec.RequireOptionValues, spec.ExcludeOptionValues} {
		for _, predicate := range predicates {
			if _, exists := known[predicate.Option]; !exists || len(predicate.Values) == 0 {
				return fmt.Errorf("option predicate %q requires a declared option and values", predicate.Option)
			}
		}
	}
	return nil
}
