package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// IsResponseAction reports whether the rule emits a model-facing response
// effect instead of an enforcement violation.
func (r *Rule) IsResponseAction() bool {
	return r.Action == ActionInject || r.Action == ActionMutate
}

// OutputText returns the static response text resolved at configuration load.
func (r *Rule) OutputText() string {
	if r.output == "" {
		return r.Output
	}
	return r.output
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
	case ActionInject, ActionMutate:
		r.AuditOnly = false
	default:
		return fmt.Errorf(
			"unknown action %q (expected %q, %q, %q, or %q)",
			r.Action, ActionBlock, ActionAudit, ActionInject, ActionMutate,
		)
	}
	return nil
}

func resolveRuleOutput(log *slog.Logger, r *Rule, configDirectory string) error {
	if !r.IsResponseAction() {
		if r.Output != "" || r.OutputFile != "" {
			return fmt.Errorf("output and output_file require action=%q or action=%q", ActionInject, ActionMutate)
		}
		return nil
	}
	if r.Output != "" && r.OutputFile != "" {
		return errors.New("output and output_file are mutually exclusive")
	}
	r.output = r.Output
	if r.OutputFile != "" {
		if filepath.IsAbs(r.OutputFile) {
			return errors.New("output_file must be relative to the config file")
		}
		path := filepath.Join(configDirectory, r.OutputFile)
		contents, err := os.ReadFile(path)
		if err != nil {
			log.Error("read rule response output file failed", "rule", r.Name, "path", path, "err", err)
			return fmt.Errorf("read output_file %q: %w", r.OutputFile, err)
		}
		r.output = string(contents)
	}
	if strings.TrimSpace(r.output) != "" || ruleHasExecCondition(r) {
		return nil
	}
	return errors.New("inject and mutate rules require output, output_file, or an exec condition")
}

func ruleHasExecCondition(r *Rule) bool {
	for index := range r.Conditions {
		if ConditionKind(r.Conditions[index].Kind) == ConditionKindExec {
			return true
		}
	}
	return false
}
