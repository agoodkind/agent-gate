package config

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/agent-gate/internal/regex"
)

var (
	errUnknownConditionKind = errors.New("unknown kind")
	errGitRefMoveFields     = errors.New("git_ref_move does not accept field_paths")
)

// compileCondition fills in compiled regex and selector state for one
// condition, validates the kind, and parses any field_pair value.
func compileCondition(
	log *slog.Logger,
	ruleName string,
	index int,
	condition *Condition,
	configDirectory string,
	cfg *Config,
) error {
	condition.selectors = CompileFieldSelectorSpecs(condition.FieldPaths)
	if condition.Kind == "" {
		condition.Kind = "regex"
	}

	switch ConditionKind(condition.Kind) {
	case ConditionKindRegex, ConditionKindCommand, ConditionKindProject, ConditionKindDiff,
		ConditionKindShellRead, ConditionKindShellWrite, ConditionKindExec,
		ConditionKindGitDefaultBranch,
		ConditionKindGitPrimaryCheckout, ConditionKindGitRefMove, ConditionKindInfer:
	default:
		log.Error(
			"compile condition kind failed",
			"rule",
			ruleName,
			"condition_index",
			index,
			"err",
			errUnknownConditionKind,
		)

		return fmt.Errorf(
			"rule %q condition %d: %w %q",
			ruleName,
			index,
			errUnknownConditionKind,
			condition.Kind,
		)
	}

	if condition.Pattern != "" {
		compiledPattern, err := regex.Compile(condition.Pattern)
		if err != nil {
			log.Error(
				"compile rule condition pattern failed",
				"rule",
				ruleName,
				"condition_index",
				index,
				"pattern",
				condition.Pattern,
				"err",
				err,
			)

			return fmt.Errorf(
				"rule %q condition %d: compile pattern %q: %w",
				ruleName,
				index,
				condition.Pattern,
				err,
			)
		}

		condition.compiled = compiledPattern
	}

	if err := validateDiagnosticGroup(
		fmt.Sprintf("rule %q condition %d", ruleName, index),
		condition.DiagnosticGroup,
		condition.compiled,
	); err != nil {
		return err
	}

	if condition.NotPattern != "" {
		compiledPattern, err := regex.Compile(condition.NotPattern)
		if err != nil {
			log.Error(
				"compile rule condition not_pattern failed",
				"rule",
				ruleName,
				"condition_index",
				index,
				"not_pattern",
				condition.NotPattern,
				"err",
				err,
			)

			return fmt.Errorf(
				"rule %q condition %d: compile not_pattern %q: %w",
				ruleName,
				index,
				condition.NotPattern,
				err,
			)
		}

		condition.compiledNot = compiledPattern
	}

	return compileConditionFields(log, ruleName, index, condition, configDirectory, cfg)
}

func compileConditionPath(
	log *slog.Logger,
	ruleName string,
	index int,
	condition *Condition,
) error {
	if condition.PathPattern != "" {
		compiledPattern, err := regex.Compile(condition.PathPattern)
		if err != nil {
			log.Error(
				"compile rule condition path_pattern failed",
				"rule",
				ruleName,
				"condition_index",
				index,
				"path_pattern",
				condition.PathPattern,
				"err",
				err,
			)

			return fmt.Errorf(
				"rule %q condition %d: compile path_pattern %q: %w",
				ruleName,
				index,
				condition.PathPattern,
				err,
			)
		}

		condition.compiledPath = compiledPattern
	}

	return nil
}

func compileConditionFields(
	log *slog.Logger,
	ruleName string,
	index int,
	condition *Condition,
	configDirectory string,
	cfg *Config,
) error {
	if err := compileConditionPath(log, ruleName, index, condition); err != nil {
		return err
	}

	if condition.FieldPair != "" {
		pair, err := ParseFieldPair(condition.FieldPair)
		if err != nil {
			log.Error(
				"compile condition field pair failed",
				"rule",
				ruleName,
				"condition_index",
				index,
				"err",
				err,
			)

			return fmt.Errorf("rule %q condition %d: %w", ruleName, index, err)
		}

		condition.fieldPairs = []FieldPairSpec{pair}
	}

	if err := validateShellReadSpecConfig(ruleName, index, condition); err != nil {
		return err
	}

	if err := compileExecConfig(ruleName, index, condition, cfg); err != nil {
		return err
	}

	if ConditionKind(condition.Kind) == ConditionKindExec &&
		condition.CacheTTLMs > 0 &&
		containsTemporalExecSelector(condition.selectors) {
		log.Warn(
			"exec condition caches process-local temporal context",
			"rule",
			ruleName,
			"condition_index",
			index,
		)
	}

	if err := compileInferConfig(
		log,
		ruleName,
		index,
		condition,
		configDirectory,
		cfg,
	); err != nil {
		return err
	}

	if err := validateShellWriteSpecConfig(ruleName, index, condition); err != nil {
		return err
	}

	if err := ValidateSelectorPolicies(
		ruleName,
		index,
		condition.ProsePolicy,
		condition.RedirectionPolicy,
	); err != nil {
		return err
	}

	if err := ValidateEnumerationSpecs(ruleName, index, condition.EnumerationSpecs); err != nil {
		return err
	}

	if ConditionKind(condition.Kind) == ConditionKindGitRefMove && len(condition.FieldPaths) > 0 {
		log.Error(
			"validate git_ref_move fields failed",
			"rule",
			ruleName,
			"condition_index",
			index,
			"err",
			errGitRefMoveFields,
		)

		return fmt.Errorf("rule %q condition %d: %w", ruleName, index, errGitRefMoveFields)
	}

	return nil
}
