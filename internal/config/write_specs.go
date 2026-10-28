package config

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
)

// ShellWriteSpec describes how a declared command exposes filesystem write
// targets. Command policy stays in configuration; this type only describes
// argv mechanics.
type ShellWriteSpec struct {
	Argv0               []string      `toml:"argv0"`
	TargetMode          string        `toml:"target_mode"`
	SkipFlagsWithValues []string      `toml:"skip_flags_with_values"`
	EndOfOptions        bool          `toml:"end_of_options"`
	CwdFlags            []string      `toml:"cwd_flags"`
	Arguments           *ArgumentSpec `toml:"arguments"`
	Unresolved          string        `toml:"unresolved"`
	TargetOptions       []string      `toml:"target_options"`
}

// Shell write target modes select operands after flags and their values have
// been removed.
const (
	WriteTargetAllOperands = "all_operands"
	WriteTargetLastOperand = "last_operand"
)

func validateShellWriteSpecConfig(ruleName string, index int, condition *Condition) error {
	if len(condition.WriteSpecs) == 0 {
		return nil
	}
	if !conditionConsumesCmdWriteTargets(condition) {
		return fmt.Errorf("rule %q condition %d write_specs 0: write_specs require cmd_write_targets", ruleName, index)
	}
	for specIndex := range condition.WriteSpecs {
		if err := validateShellWriteSpec(&condition.WriteSpecs[specIndex]); err != nil {
			slog.Error("invalid write description", "rule", ruleName, "condition", index, "write_spec", specIndex, "err", err)
			return fmt.Errorf("rule %q condition %d write_specs %d: %w", ruleName, index, specIndex, err)
		}
	}
	return nil
}

func validateShellWriteSpec(spec *ShellWriteSpec) error {
	if len(spec.Argv0) == 0 {
		return fmt.Errorf("argv0 is required")
	}
	for argvIndex := range spec.Argv0 {
		spec.Argv0[argvIndex] = strings.TrimSpace(spec.Argv0[argvIndex])
		if spec.Argv0[argvIndex] == "" {
			return fmt.Errorf("argv0 entries must be non-empty")
		}
	}
	if spec.TargetMode != WriteTargetAllOperands && spec.TargetMode != WriteTargetLastOperand {
		return fmt.Errorf("unknown target_mode %q", spec.TargetMode)
	}
	if !nonEmptyFlagEntries(spec.SkipFlagsWithValues) || !nonEmptyFlagEntries(spec.CwdFlags) {
		return fmt.Errorf("flag entries must be non-empty")
	}
	if !flagEntries(spec.SkipFlagsWithValues) {
		return fmt.Errorf("skip_flags_with_values entries must start with '-'")
	}
	if !flagEntries(spec.CwdFlags) {
		return fmt.Errorf("cwd_flags entries must start with '-'")
	}
	for _, flag := range spec.SkipFlagsWithValues {
		if slices.Contains(spec.CwdFlags, flag) {
			return fmt.Errorf("flag %q must not appear in both skip_flags_with_values and cwd_flags", flag)
		}
	}
	if spec.Unresolved != "" && spec.Unresolved != "match" && spec.Unresolved != "ignore" {
		return fmt.Errorf("unresolved must be match or ignore")
	}
	arguments := spec.ArgumentDescription()
	if err := ValidateArgumentSpec(&arguments); err != nil {
		return err
	}
	return validateWriteTargetOptions(spec.TargetOptions, arguments.Options)
}

func validateWriteTargetOptions(names []string, options []ArgumentOptionSpec) error {
	for _, name := range names {
		declared := false
		for _, option := range options {
			if slices.Contains(option.Names, name) && option.Value != "none" {
				declared = true
			}
		}
		if !declared {
			return fmt.Errorf("target option %q must accept a declared value", name)
		}
	}
	return nil
}

func conditionConsumesCmdWriteTargets(condition *Condition) bool {
	switch ConditionKind(condition.Kind) {
	case ConditionKindShellWrite:
		return true
	case ConditionKindRegex, ConditionKindExec, ConditionKindGitDefaultBranch,
		ConditionKindGitPrimaryCheckout, ConditionKindInfer:
	case ConditionKindCommand,
		ConditionKindDiff,
		ConditionKindProject,
		ConditionKindShellRead,
		ConditionKindGitRefMove:
		return false
	}
	for _, selector := range condition.selectors {
		if selector.Selector == FieldCmdWriteTargets {
			return true
		}
	}
	return condition.cacheKeySelector.Selector == FieldCmdWriteTargets ||
		condition.forEachSelector.Selector == FieldCmdWriteTargets ||
		condition.inputSelector.Selector == FieldCmdWriteTargets
}

// ArgumentDescription normalizes legacy declarations into shared argument roles.
func (spec ShellWriteSpec) ArgumentDescription() ArgumentSpec {
	var arguments ArgumentSpec
	arguments.EndOfOptions = spec.EndOfOptions
	arguments.UnknownOptions = "skip"
	if spec.Arguments != nil {
		arguments = *spec.Arguments
		arguments.Options = slices.Clone(spec.Arguments.Options)
		arguments.EndOfOptions = arguments.EndOfOptions || spec.EndOfOptions
	}
	for _, flag := range spec.SkipFlagsWithValues {
		arguments.Options = append(arguments.Options, ArgumentOptionSpec{Names: []string{flag}, Value: "required", Forms: []string{"separate", "equals"}, CWD: false, AttachedValuePattern: ""})
	}
	for _, flag := range spec.CwdFlags {
		arguments.Options = append(arguments.Options, ArgumentOptionSpec{Names: []string{flag}, Value: "required", Forms: []string{"separate", "equals"}, CWD: true, AttachedValuePattern: ""})
	}
	return arguments
}

func nonEmptyFlagEntries(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func flagEntries(values []string) bool {
	for _, value := range values {
		if !strings.HasPrefix(value, "-") {
			return false
		}
	}
	return true
}
