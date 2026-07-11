package config

import (
	"fmt"
	"slices"
	"strings"
)

// ShellWriteSpec describes how a declared command exposes filesystem write
// targets. Command policy stays in configuration; this type only describes
// argv mechanics.
type ShellWriteSpec struct {
	Argv0               []string `toml:"argv0"`
	TargetMode          string   `toml:"target_mode"`
	SkipFlagsWithValues []string `toml:"skip_flags_with_values"`
	EndOfOptions        bool     `toml:"end_of_options"`
	CwdFlags            []string `toml:"cwd_flags"`
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
		spec := &condition.WriteSpecs[specIndex]
		if len(spec.Argv0) == 0 {
			return fmt.Errorf("rule %q condition %d write_specs %d: argv0 is required", ruleName, index, specIndex)
		}
		for argvIndex := range spec.Argv0 {
			spec.Argv0[argvIndex] = strings.TrimSpace(spec.Argv0[argvIndex])
			if spec.Argv0[argvIndex] == "" {
				return fmt.Errorf("rule %q condition %d write_specs %d: argv0 entries must be non-empty", ruleName, index, specIndex)
			}
		}
		switch spec.TargetMode {
		case WriteTargetAllOperands, WriteTargetLastOperand:
		default:
			return fmt.Errorf("rule %q condition %d write_specs %d: unknown target_mode %q", ruleName, index, specIndex, spec.TargetMode)
		}
		if !nonEmptyFlagEntries(spec.SkipFlagsWithValues) || !nonEmptyFlagEntries(spec.CwdFlags) {
			return fmt.Errorf("rule %q condition %d write_specs %d: flag entries must be non-empty", ruleName, index, specIndex)
		}
		if !flagEntries(spec.SkipFlagsWithValues) {
			return fmt.Errorf("rule %q condition %d write_specs %d: skip_flags_with_values entries must start with '-'", ruleName, index, specIndex)
		}
		if !flagEntries(spec.CwdFlags) {
			return fmt.Errorf("rule %q condition %d write_specs %d: cwd_flags entries must start with '-'", ruleName, index, specIndex)
		}
		for _, flag := range spec.SkipFlagsWithValues {
			if slices.Contains(spec.CwdFlags, flag) {
				return fmt.Errorf("rule %q condition %d write_specs %d: flag %q must not appear in both skip_flags_with_values and cwd_flags", ruleName, index, specIndex, flag)
			}
		}
	}
	return nil
}

func conditionConsumesCmdWriteTargets(condition *Condition) bool {
	switch ConditionKind(condition.Kind) {
	case ConditionKindRegex, ConditionKindExec, ConditionKindGitDefaultBranch,
		ConditionKindGitPrimaryCheckout:
	case ConditionKindCommand,
		ConditionKindDiff,
		ConditionKindProject,
		ConditionKindShellRead,
		ConditionKindShellWrite,
		ConditionKindComposer,
		ConditionKindGitRefMove:
		return false
	}
	for _, selector := range condition.selectors {
		if selector.Selector == FieldCmdWriteTargets {
			return true
		}
	}
	return condition.cacheKeySelector.Selector == FieldCmdWriteTargets ||
		condition.forEachSelector.Selector == FieldCmdWriteTargets
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
