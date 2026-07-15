package config

import (
	"fmt"
	"log/slog"
)

// Evaluator kinds for a [RuleEval] entry.
const (
	// EvalKindDeterministic runs the rule's ordinary condition block.
	EvalKindDeterministic = "deterministic"
	// EvalKindInfer runs an inference point against the rule.
	EvalKindInfer = "infer"
)

// Evaluator roles for a [RuleEval] entry. The role decides whether an evaluator
// enforces the verdict or runs only for comparison.
const (
	// RoleEnforce means this evaluator's verdict is enforced.
	RoleEnforce = "enforce"
	// RoleVerify means this evaluator runs and is recorded, but does not enforce.
	RoleVerify = "verify"
)

// Fan-out modes for an infer [RuleEval] entry.
const (
	// FanoutBatch evaluates a request's opted-in rules in one inference call.
	FanoutBatch = "batch"
	// FanoutIndividual evaluates each rule in its own inference call.
	FanoutIndividual = "individual"
)

// Combine operators for how an evaluator's verdict joins the rule decision.
const (
	// CombineAnd blocks only when every enforcing evaluator blocks.
	CombineAnd = "and"
	// CombineUnion blocks when any enforcing evaluator blocks.
	CombineUnion = "union"
)

// An infer [RuleEval] entry reuses the shared OnErrorOpen and OnErrorClosed
// values to decide its verdict when the inference call fails. OnErrorOpen allows
// the command (fail open, with the deterministic evaluators as the backstop), and
// OnErrorClosed blocks it (fail closed).

// Confidence sources for an [InferencePoint].
const (
	// ConfidenceOutputField reads a confidence value from the model's JSON output.
	ConfidenceOutputField = "output_field"
	// ConfidenceLogprob derives confidence from the decision token's logprob.
	ConfidenceLogprob = "logprob"
)

// InferencePoint is a named, reusable inference backend plus its confidence
// policy. Rules reference a point by name through a [RuleEval] so the endpoint,
// model, and thresholds are declared once and shared.
type InferencePoint struct {
	Endpoint            string  `toml:"endpoint"`
	Model               string  `toml:"model"`
	ReasoningEffort     string  `toml:"reasoning_effort,omitempty"`
	ConfidenceSource    string  `toml:"confidence_source,omitempty"`
	ConfidenceField     string  `toml:"confidence_field,omitempty"`
	ConfidenceThreshold float64 `toml:"confidence_threshold,omitempty"`
	TimeoutMs           int     `toml:"timeout_ms,omitempty"`
	// Conversation-context knobs. When ContextEndpoint is set, a batch judge
	// fetches recent turns from that context service and passes them to the model,
	// so it can tell an intended action from an actual write. ContextOnError chooses
	// whether a context fetch failure still judges on the command alone (open) or
	// is treated as an inference error (closed).
	ContextEndpoint        string `toml:"context_endpoint,omitempty"`
	ContextWorkspaceField  string `toml:"context_workspace_field,omitempty"`
	ContextSessionField    string `toml:"context_session_field,omitempty"`
	ContextTurnBudget      int    `toml:"context_turn_budget,omitempty"`
	ContextMaxCharsPerTurn int    `toml:"context_max_chars_per_turn,omitempty"`
	ContextOnError         string `toml:"context_on_error,omitempty"`
}

// RuleEval declares one evaluator in a rule's ordered evaluation. The ordering of
// entries plus each entry's role expresses how the deterministic layer and the
// inference layer combine for the rule, without a fixed mode enum. OnError applies
// to an infer entry and decides its verdict when the inference call fails.
type RuleEval struct {
	Kind    string `toml:"kind"`
	Role    string `toml:"role"`
	Use     string `toml:"use,omitempty"`
	Fanout  string `toml:"fanout,omitempty"`
	Combine string `toml:"combine,omitempty"`
	OnError string `toml:"on_error,omitempty"`
}

// validateInferencePoints checks every named inference point in isolation and
// returns the first problem found. It logs the offending point before returning
// so a failed config load leaves a diagnostic trail.
func validateInferencePoints(log *slog.Logger, points map[string]InferencePoint) error {
	for name, point := range points {
		problem := inferencePointProblem(point)
		if problem == "" {
			continue
		}
		log.Warn("invalid inference point", "name", name, "problem", problem)
		return fmt.Errorf("inference point %q: %s", name, problem)
	}
	return nil
}

// inferencePointProblem returns a human-readable problem string for an invalid
// inference point, or an empty string when the point is valid.
func inferencePointProblem(point InferencePoint) string {
	switch {
	case point.Endpoint == "":
		return "endpoint is required"
	case point.Model == "":
		return "model is required"
	case !validConfidenceSource(point.ConfidenceSource):
		return fmt.Sprintf(
			"confidence_source %q must be %q, %q, or empty",
			point.ConfidenceSource, ConfidenceOutputField, ConfidenceLogprob,
		)
	case point.ConfidenceSource == ConfidenceOutputField && point.ConfidenceField == "":
		return "confidence_field is required when confidence_source is output_field"
	case point.ConfidenceThreshold < 0 || point.ConfidenceThreshold > 1:
		return fmt.Sprintf(
			"confidence_threshold %v must be within [0, 1]",
			point.ConfidenceThreshold,
		)
	case point.ContextTurnBudget < 0:
		return fmt.Sprintf("context_turn_budget %d must be non-negative", point.ContextTurnBudget)
	case point.ContextMaxCharsPerTurn < 0:
		return fmt.Sprintf("context_max_chars_per_turn %d must be non-negative", point.ContextMaxCharsPerTurn)
	case !validContextOnError(point.ContextOnError):
		return fmt.Sprintf(
			"context_on_error %q must be %q, %q, or empty",
			point.ContextOnError, OnErrorOpen, OnErrorClosed,
		)
	case point.ContextEndpoint == "" && point.ContextWorkspaceField != "":
		return "context_workspace_field is set without context_endpoint"
	default:
		return ""
	}
}

func validConfidenceSource(source string) bool {
	return source == "" || source == ConfidenceOutputField || source == ConfidenceLogprob
}

func validContextOnError(onError string) bool {
	return onError == "" || onError == OnErrorOpen || onError == OnErrorClosed
}

// compileRuleEvalInference resolves the inference points a rule's Eval entries
// reference into Rule.EvalInference and requires an intent when the rule declares
// an infer evaluator. It runs after validateRuleEval, so every referenced point
// is known to exist.
func compileRuleEvalInference(rule *Rule, points map[string]InferencePoint) error {
	rule.EvalInference = nil
	hasInfer := false
	resolved := map[string]InferencePoint{}
	for index := range rule.Eval {
		eval := rule.Eval[index]
		if eval.Kind != EvalKindInfer {
			continue
		}
		hasInfer = true
		resolved[eval.Use] = points[eval.Use]
	}
	if hasInfer && rule.Intent == "" {
		return fmt.Errorf("rule %q: an infer evaluator requires intent", rule.Name)
	}
	if len(resolved) > 0 {
		rule.EvalInference = resolved
	}
	return nil
}

// validateRuleEval checks a rule's evaluator list against the declared inference
// points. It confirms kinds, roles, fan-out, and combine values, and that every
// referenced inference point exists. It logs the offending entry before
// returning so a failed config load leaves a diagnostic trail.
func validateRuleEval(
	log *slog.Logger,
	ruleName string,
	evals []RuleEval,
	points map[string]InferencePoint,
) error {
	for index, eval := range evals {
		problem := evalEntryProblem(eval, points)
		if problem == "" {
			continue
		}
		log.Warn("invalid rule eval", "rule", ruleName, "index", index, "problem", problem)
		return fmt.Errorf("rule %q eval %d: %s", ruleName, index, problem)
	}
	return nil
}

// validateRuleEvalConditions rejects a deterministic evaluator on a rule that
// declares no conditions. A deterministic evaluator runs the rule's condition
// block, so with an empty condition list allConditionsMatch is vacuously true and
// the evaluator would block every command, ignoring the rule's own pattern
// matcher. It logs the offending rule before returning so a failed config load
// leaves a diagnostic trail.
func validateRuleEvalConditions(
	log *slog.Logger,
	ruleName string,
	evals []RuleEval,
	conditionCount int,
) error {
	if conditionCount > 0 {
		return nil
	}
	for index := range evals {
		if evals[index].Kind != EvalKindDeterministic {
			continue
		}
		log.Warn("deterministic evaluator without conditions", "rule", ruleName, "index", index)
		return fmt.Errorf(
			"rule %q eval %d: a deterministic evaluator requires the rule to declare conditions",
			ruleName, index,
		)
	}
	return nil
}

// evalEntryProblem returns a human-readable problem string for an invalid
// evaluator entry, or an empty string when the entry is valid.
func evalEntryProblem(eval RuleEval, points map[string]InferencePoint) string {
	if problem := evalEntryShapeProblem(eval); problem != "" {
		return problem
	}
	if eval.Kind == EvalKindDeterministic {
		if eval.Use != "" || eval.Fanout != "" || eval.OnError != "" {
			return "deterministic evaluator does not accept use, fanout, or on_error"
		}
		return ""
	}
	return inferEntryReferenceProblem(eval, points)
}

// evalEntryShapeProblem validates the enum-valued fields shared by every
// evaluator entry.
func evalEntryShapeProblem(eval RuleEval) string {
	if eval.Kind != EvalKindDeterministic && eval.Kind != EvalKindInfer {
		return fmt.Sprintf("kind %q must be %q or %q", eval.Kind, EvalKindDeterministic, EvalKindInfer)
	}
	if eval.Role != RoleEnforce && eval.Role != RoleVerify {
		return fmt.Sprintf("role %q must be %q or %q", eval.Role, RoleEnforce, RoleVerify)
	}
	if eval.Fanout != "" && eval.Fanout != FanoutBatch && eval.Fanout != FanoutIndividual {
		return fmt.Sprintf("fanout %q must be %q, %q, or empty", eval.Fanout, FanoutBatch, FanoutIndividual)
	}
	if eval.Combine != "" && eval.Combine != CombineAnd && eval.Combine != CombineUnion {
		return fmt.Sprintf("combine %q must be %q, %q, or empty", eval.Combine, CombineAnd, CombineUnion)
	}
	if eval.OnError != "" && eval.OnError != OnErrorOpen && eval.OnError != OnErrorClosed {
		return fmt.Sprintf("on_error %q must be %q, %q, or empty", eval.OnError, OnErrorOpen, OnErrorClosed)
	}
	return ""
}

// inferEntryReferenceProblem validates that an infer evaluator names a declared
// inference point for its use reference.
func inferEntryReferenceProblem(eval RuleEval, points map[string]InferencePoint) string {
	if eval.Use == "" {
		return "infer evaluator requires use"
	}
	if _, ok := points[eval.Use]; !ok {
		return fmt.Sprintf("use %q is not a declared inference point", eval.Use)
	}
	return ""
}
