package rules

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"sync/atomic"

	"goodkind.io/agent-gate/internal/composer"
	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
	"goodkind.io/agent-gate/internal/regex"
	diffconcern "goodkind.io/agent-gate/internal/rules/concerns/diff"
	concernlimit "goodkind.io/agent-gate/internal/rules/concerns/limit"
	regexconcern "goodkind.io/agent-gate/internal/rules/concerns/regex"
	shellreadconcern "goodkind.io/agent-gate/internal/rules/concerns/shellread"
	shellwriteconcern "goodkind.io/agent-gate/internal/rules/concerns/shellwrite"
	"goodkind.io/agent-gate/pipeline"
	"goodkind.io/gksyntax/shelldecomp"
)

// Violation describes one concrete match that violated a rule. The location
// fields (FieldPath, FilePath, Value, Start, End) carry the match position
// when available; they stay empty for rule classes that have no specific span
// to point at (such as fallback violations from gate-only condition rules).
type Violation struct {
	RuleName         string
	Message          string
	AuditOnly        bool
	Redact           bool
	DiagnosticFormat string
	FieldPath        string
	FilePath         string
	Value            string
	Start            int
	End              int
}

// Evaluate iterates over all rules and returns the first Violation whose
// pattern matches a typed field selected from payload, or nil if no rule fires.
func Evaluate(ctx context.Context, system, eventName string, fields FieldSet, rules []config.Rule) *Violation {
	violations := EvaluateAll(ctx, system, eventName, fields, rules, nil)
	if len(violations) == 0 {
		return nil
	}
	out := violations[0]
	return &out
}

// EvaluateAll returns every concrete match for every applicable rule across
// all condition kinds. One pipeline.Condition is built per applicable regex
// unit: simple rules get one Condition each; condition-based rules get one
// Condition per regex/diff/shell-write-kind condition. Command and project
// conditions remain inline (landings 6 and 7 will migrate those).
//
// getenv is consulted by the [config.Rule.DisableIfEnv] guard. Pass nil to
// disable env-based rule skipping.
func EvaluateAll(ctx context.Context, system, eventName string, fields FieldSet, rulesSlice []config.Rule, getenv func(string) string) []Violation {
	conditions := buildRuleRegexConditions(&fields, rulesSlice, system, eventName, getenv)
	if len(conditions) == 0 {
		return nil
	}
	memo := newExecEventMemo(system, eventName)
	evalCtx := withExecEventMemo(ctx, memo)
	orch := &pipeline.Orchestrator{
		Conditions: conditions,
		Scheduler:  pipeline.FixedScheduler{SlotCount: 1},
		Sentinel:   pipeline.NoopSentinel{},
	}
	results, _ := orch.Run(evalCtx, pipeline.Input{})
	return aggregateResults(results, fields, memo)
}

// envGuardFires returns true when getenv reports any of keys as non-empty.
// A nil getenv or empty keys slice always returns false so rules without a
// [config.Rule.DisableIfEnv] field are unaffected.
func envGuardFires(getenv func(string) string, keys []string) bool {
	if getenv == nil || len(keys) == 0 {
		return false
	}
	for _, key := range keys {
		if getenv(key) != "" {
			return true
		}
	}
	return false
}

// aggregateResults flattens Orchestrator results into Violation values.
// For condition-based rules where the gate fired but all regex conditions produced
// zero matches, one fallback violation is emitted per rule.
func aggregateResults(results []pipeline.Result, fields FieldSet, memo *execEventMemo) []Violation {
	// Track which condition-based rules fired but produced no regex matches.
	// Key is rule pointer (same slice element across conditions for one rule).
	type ruleState struct {
		rule        *config.Rule
		matchedGate bool
		matchCount  int
	}
	var gateRules []*ruleState
	ruleStateByPtr := map[*config.Rule]*ruleState{}

	var violations []Violation
	for i := range results {
		if results[i].Outcome == nil {
			continue
		}
		out, ok := results[i].Outcome.(ruleOutcome)
		if !ok {
			continue
		}
		if out.rule != nil && out.isConditionBased {
			rs, exists := ruleStateByPtr[out.rule]
			if !exists {
				rs = &ruleState{rule: out.rule, matchedGate: false, matchCount: 0}
				ruleStateByPtr[out.rule] = rs
				gateRules = append(gateRules, rs)
			}
			if out.gateMatched {
				rs.matchedGate = true
				rs.matchCount += len(out.violations)
			}
		}
		violations = append(violations, out.violations...)
	}

	// Emit fallback for condition-based rules where the gate matched but no regex
	// conditions produced a violation.
	for _, rs := range gateRules {
		if rs.matchedGate && rs.matchCount == 0 {
			violations = append(violations, conditionFallbackViolation(fields, rs.rule))
		}
	}
	applyExecMessageOverrides(violations, memo)
	return violations
}

// applyExecMessageOverrides rewrites the message of every violation whose rule
// had a blocking exec validator emit a stdout line, so the script's specific
// reason replaces the static violation_message for that event.
func applyExecMessageOverrides(violations []Violation, memo *execEventMemo) {
	if memo == nil {
		return
	}
	for i := range violations {
		if msg, ok := memo.overrideFor(violations[i].RuleName); ok && msg != "" {
			violations[i].Message = msg
		}
	}
}

// buildRuleRegexConditions builds one Condition per evaluation unit.
// Simple rules contribute one Condition each.
// Condition-based rules with at least one matching condition contribute one
// Condition per regex/diff/shell-write-kind condition.
// Condition-based rules with only non-matching conditions (command, project)
// contribute one Condition that checks the gate and returns the fallback
// violation on match.
// Rules whose [config.Rule.DisableIfEnv] guard fires are skipped.
// Non-applicable rules are skipped.
// fields is passed by pointer so all Conditions share the single EvaluateAll-owned copy.
func buildRuleRegexConditions(fields *FieldSet, rulesSlice []config.Rule, system, eventName string, getenv func(string) string) []pipeline.Condition {
	var conditions []pipeline.Condition
	for i := range rulesSlice {
		rule := &rulesSlice[i]
		budget := newRuleMatchBudget(concernlimit.MaxCollectedMatchesPerEvaluation)
		if !appliesToEvent(rule, system, eventName) {
			continue
		}
		if envGuardFires(getenv, rule.DisableIfEnv) {
			continue
		}
		if len(rule.Conditions) == 0 {
			conditions = append(conditions, &ruleRegexCondition{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: -1,
				budget:  budget,
			})
			continue
		}

		matchingCondCount := 0
		for j := range rule.Conditions {
			if !isMatchingConditionKind(&rule.Conditions[j]) {
				continue
			}
			if conditionKind(&rule.Conditions[j]) == config.ConditionKindRegex && rule.Conditions[j].CompiledPattern() == nil {
				continue
			}
			conditions = append(conditions, &ruleRegexCondition{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: j,
				budget:  budget,
			})
			matchingCondCount++
		}

		if matchingCondCount == 0 {
			// Rule has only gate-only conditions (command, project). One
			// fallback Condition represents it.
			conditions = append(conditions, &ruleRegexCondition{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: -2,
				budget:  nil,
			})
		}
	}
	return conditions
}

// isMatchingConditionKind reports whether c is a kind that produces concrete
// match spans (regex, diff, shell-write) rather than a gate-only kind
// (command, project) handled by [allConditionsMatch] alone. The exhaustive
// switch ensures any new kind added to [config.ConditionKind] forces an
// explicit decision here.
func isMatchingConditionKind(c *config.Condition) bool {
	switch conditionKind(c) {
	case config.ConditionKindRegex, config.ConditionKindDiff, config.ConditionKindShellRead, config.ConditionKindShellWrite:
		return true
	case config.ConditionKindCommand, config.ConditionKindProject, config.ConditionKindExec,
		config.ConditionKindComposer, config.ConditionKindGitDefaultBranch,
		config.ConditionKindGitPrimaryCheckout, config.ConditionKindGitRefMove:
		// Gate-only kinds. They must pass for the rule to fire but they do
		// not by themselves emit per-match diagnostics, so they are evaluated
		// as part of the gate inside [allConditionsMatch] and surfaced via
		// the fallback Condition (condIdx == -2) rather than a per-condition
		// Condition.
		return false
	default:
		return false
	}
}

// ruleRegexCondition is a pipeline.Condition for one regex evaluation unit.
// condIdx == -1 means this is a simple rule evaluated against its top-level pattern.
// condIdx >= 0 means this is one regex-kind condition of a condition-based rule.
// fields is a pointer to the EvaluateAll-owned copy to avoid N redundant copies.
type ruleRegexCondition struct {
	name    string
	fields  *FieldSet
	rule    *config.Rule
	condIdx int
	budget  *ruleMatchBudget
}

type ruleMatchBudget struct {
	remaining atomic.Int32
}

func newRuleMatchBudget(limit int) *ruleMatchBudget {
	budget := new(ruleMatchBudget)
	if limit <= 0 {
		budget.remaining.Store(0)
		return budget
	}
	if limit > math.MaxInt32 {
		budget.remaining.Store(math.MaxInt32)
		return budget
	}
	budget.remaining.Store(int32(limit))
	return budget
}

func (b *ruleMatchBudget) Remaining() int {
	if b == nil {
		return 0
	}

	remaining := b.remaining.Load()
	if remaining < 0 {
		return 0
	}
	return int(remaining)
}

func (b *ruleMatchBudget) Consume(count int) {
	if b == nil || count <= 0 {
		return
	}

	for {
		current := b.remaining.Load()
		if current <= 0 {
			return
		}

		if count > math.MaxInt32 {
			if b.remaining.CompareAndSwap(current, 0) {
				return
			}
			continue
		}

		decrement := int32(count)
		next := max(current-decrement, 0)
		if b.remaining.CompareAndSwap(current, next) {
			return
		}
	}
}

// ruleOutcome carries the violations produced by one ruleRegexCondition plus the
// metadata that aggregateResults needs to deduplicate fallback generation.
type ruleOutcome struct {
	violations       []Violation
	rule             *config.Rule
	isConditionBased bool
	gateMatched      bool
}

func (ruleOutcome) PipelineOutcome() {}

func (r *ruleRegexCondition) Profile() pipeline.Profile {
	return pipeline.Profile{
		Name:         r.name,
		Cost:         pipeline.CostCheap,
		Idempotent:   true,
		MemoLifetime: pipeline.MemoEvent,
	}
}

func (r *ruleRegexCondition) Execute(ctx context.Context, _ pipeline.Input) (pipeline.Outcome, error) {
	if r.condIdx == -1 {
		return ruleOutcome{
			violations:       evalSimpleRule(r.fields, r.rule, r.budget.Remaining()),
			rule:             nil,
			isConditionBased: false,
			gateMatched:      false,
		}, nil
	}
	// condIdx == -2: rule whose conditions are all non-regex (command, project).
	// The fallback violation is returned inline when the gate passes; aggregateResults
	// sees matchCount > 0 and does not add another fallback.
	if r.condIdx == -2 {
		if !allConditionsMatch(ctx, *r.fields, r.rule, r.rule.Conditions) {
			return ruleOutcome{
				violations:       nil,
				rule:             r.rule,
				isConditionBased: true,
				gateMatched:      false,
			}, nil
		}
		return ruleOutcome{
			violations:       []Violation{conditionFallbackViolation(*r.fields, r.rule)},
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	}
	// condIdx >= 0: one matching-kind condition. The full gate must pass before
	// matches are evaluated. aggregateResults adds a single fallback when all
	// matching conditions for the rule produce zero violations in aggregate.
	if !allConditionsMatch(ctx, *r.fields, r.rule, r.rule.Conditions) {
		return ruleOutcome{
			violations:       nil,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      false,
		}, nil
	}
	c := &r.rule.Conditions[r.condIdx]
	matchLimit := r.budget.Remaining()
	if matchLimit == 0 {
		return ruleOutcome{
			violations:       nil,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	}
	switch conditionKind(c) {
	case config.ConditionKindRegex:
		accessor := conditionFieldAccessor{fields: r.fields, condition: c}
		matches := regexconcern.EvalFieldMatches(accessor, c.Selectors(), c.CompiledPattern(), c.DiagnosticGroup, matchLimit)
		r.budget.Consume(len(matches))
		return ruleOutcome{
			violations:       matchesToViolations(matches, r.rule),
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	case config.ConditionKindDiff:
		violations := evalDiffCondition(r.fields, c, r.rule, matchLimit)
		r.budget.Consume(len(violations))
		return ruleOutcome{
			violations:       violations,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	case config.ConditionKindShellWrite:
		return ruleOutcome{
			violations:       evalShellWriteCondition(r.fields, c, r.rule),
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	case config.ConditionKindShellRead:
		violations := evalShellReadCondition(r.fields, c, r.rule, matchLimit)
		r.budget.Consume(len(violations))
		return ruleOutcome{
			violations:       violations,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	case config.ConditionKindCommand, config.ConditionKindProject, config.ConditionKindExec,
		config.ConditionKindComposer, config.ConditionKindGitDefaultBranch,
		config.ConditionKindGitPrimaryCheckout, config.ConditionKindGitRefMove:
		// Gate-only kinds are handled by [allConditionsMatch] above and
		// produce no per-condition Condition. Reaching this arm means
		// [buildRuleRegexConditions] mis-routed a condition; emit nothing
		// rather than fabricating a violation.
		return ruleOutcome{
			violations:       nil,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	default:
		return ruleOutcome{
			violations:       nil,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	}
}

// evalDiffCondition runs the diff Condition for one condition and returns its
// match violations in the rules.Violation shape.
func evalDiffCondition(fields *FieldSet, c *config.Condition, rule *config.Rule, limit int) []Violation {
	if limit <= 0 {
		return nil
	}

	pairs := c.FieldPairs()
	if len(pairs) == 0 {
		// Default to old_string/new_string when field_pair is unset.
		pairs = []config.FieldPairSpec{{
			OldPath:  "tool_input.old_string",
			NewPath:  "tool_input.new_string",
			OldField: config.FieldToolInputOldString,
			NewField: config.FieldToolInputNewString,
		}}
	}
	pattern := c.CompiledPattern()
	if pattern == nil {
		return nil
	}
	group, ok := safeDiagnosticGroup(c.DiagnosticGroup)
	if !ok {
		return nil
	}
	var matches []diffconcern.MatchResult
	for _, pair := range pairs {
		remaining := limit - len(matches)
		if remaining == 0 {
			break
		}

		matches = append(matches, diffconcern.EvalDiffMatches(fields, pair, pattern, group, remaining)...)
		// Fallback: when the configured new field is empty but edits[*] has
		// content, apply the same pattern against the joined edits view so
		// a single condition covers both Edit and MultiEdit shapes.
		if len(matches) == 0 && pair.NewField == config.FieldToolInputNewString {
			remaining = limit - len(matches)
			if remaining == 0 {
				break
			}
			batchPair := config.FieldPairSpec{
				OldPath:  "edits[*].old_string",
				NewPath:  "edits[*].new_string",
				OldField: config.FieldEditsOldString,
				NewField: config.FieldEditsNewString,
			}
			matches = append(matches, diffconcern.EvalDiffMatches(fields, batchPair, pattern, group, remaining)...)
		}
	}
	return diffMatchesToViolations(matches, rule)
}

func diffMatchesToViolations(matches []diffconcern.MatchResult, rule *config.Rule) []Violation {
	if len(matches) == 0 {
		return nil
	}
	out := make([]Violation, len(matches))
	for i, m := range matches {
		out[i] = Violation{
			RuleName:         rule.Name,
			Message:          rule.ViolationMessage,
			AuditOnly:        rule.AuditOnly,
			Redact:           rule.RedactDiagnostics,
			DiagnosticFormat: rule.DiagnosticFormat,
			FieldPath:        m.FieldPath,
			FilePath:         m.FilePath,
			Value:            m.Value,
			Start:            m.Start,
			End:              m.End,
		}
	}
	return out
}

// evalShellWriteCondition runs the shellwrite Condition for one condition and
// returns its match violations in the rules.Violation shape.
func evalShellWriteCondition(fields *FieldSet, c *config.Condition, rule *config.Rule) []Violation {
	commandSelector := config.FieldToolInputCommand
	for _, sel := range c.Selectors() {
		if sel.Selector != config.FieldSelectorInvalid {
			commandSelector = sel.Selector
			break
		}
	}
	matches := shellwriteconcern.EvalShellWriteMatches(fields, commandSelector, c.Globs)
	if len(matches) == 0 {
		return nil
	}
	out := make([]Violation, len(matches))
	for i, m := range matches {
		out[i] = Violation{
			RuleName:         rule.Name,
			Message:          rule.ViolationMessage,
			AuditOnly:        rule.AuditOnly,
			Redact:           rule.RedactDiagnostics,
			DiagnosticFormat: rule.DiagnosticFormat,
			FieldPath:        m.FieldPath,
			FilePath:         m.FilePath,
			Value:            m.Value,
			Start:            m.Start,
			End:              m.End,
		}
	}
	return out
}

// evalShellReadCondition runs the shell_read_secret Condition for one
// condition and returns its match violations in the rules.Violation shape.
func evalShellReadCondition(fields *FieldSet, c *config.Condition, rule *config.Rule, limit int) []Violation {
	if limit <= 0 {
		return nil
	}
	commandSelector := config.FieldToolInputCommand
	for _, sel := range c.Selectors() {
		if sel.Selector != config.FieldSelectorInvalid {
			commandSelector = sel.Selector
			break
		}
	}
	matches := shellreadconcern.EvalShellReadSecretMatches(
		fields,
		commandSelector,
		c.CompiledPattern(),
		c.CompiledPathPattern(),
		c.MaxBytes,
		c.RemotePolicy,
		c.ReadSpecs,
	)
	if len(matches) == 0 {
		return nil
	}
	if len(matches) > limit {
		matches = matches[:limit]
	}
	out := make([]Violation, len(matches))
	for i, m := range matches {
		out[i] = Violation{
			RuleName:         rule.Name,
			Message:          rule.ViolationMessage,
			AuditOnly:        rule.AuditOnly,
			Redact:           rule.RedactDiagnostics,
			DiagnosticFormat: rule.DiagnosticFormat,
			FieldPath:        m.FieldPath,
			FilePath:         m.FilePath,
			Value:            m.Value,
			Start:            m.Start,
			End:              m.End,
		}
	}
	return out
}

// evalSimpleRule runs the top-level pattern for a rule with no conditions.
func evalSimpleRule(fields *FieldSet, rule *config.Rule, limit int) []Violation {
	if simpleRuleNotPatternMatches(fields, rule) {
		return nil
	}
	matches := regexconcern.EvalFieldMatches(fields, rule.Selectors(), rule.Compiled(), rule.DiagnosticGroup, limit)
	return matchesToViolations(matches, rule)
}

func simpleRuleNotPatternMatches(fields *FieldSet, rule *config.Rule) bool {
	notPattern := rule.CompiledNot()
	if notPattern == nil {
		return false
	}
	for _, selector := range rule.Selectors() {
		value := fields.String(selector.Selector)
		if value != "" && notPattern.MatchString(value) {
			return true
		}
	}
	return false
}

// matchesToViolations converts MatchResult values from the concern package into
// Violation values with rule metadata attached.
func matchesToViolations(matches []regexconcern.MatchResult, rule *config.Rule) []Violation {
	if len(matches) == 0 {
		return nil
	}
	violations := make([]Violation, len(matches))
	for i, m := range matches {
		violations[i] = Violation{
			RuleName:         rule.Name,
			Message:          rule.ViolationMessage,
			AuditOnly:        rule.AuditOnly,
			Redact:           rule.RedactDiagnostics,
			DiagnosticFormat: rule.DiagnosticFormat,
			FieldPath:        m.FieldPath,
			FilePath:         m.FilePath,
			Value:            m.Value,
			Start:            m.Start,
			End:              m.End,
		}
	}
	return violations
}

func conditionFallbackViolation(fields FieldSet, rule *config.Rule) Violation {
	fieldPath := "payload"
	value := rule.Name
	for i := range rule.Conditions {
		condition := &rule.Conditions[i]
		if path, extracted := fields.FirstStringForCondition(condition.Selectors(), condition); extracted != "" {
			fieldPath = path
			value = extracted
			break
		}
	}
	end := min(len(value), 1)
	return Violation{
		RuleName:         rule.Name,
		Message:          rule.ViolationMessage,
		AuditOnly:        rule.AuditOnly,
		Redact:           rule.RedactDiagnostics,
		DiagnosticFormat: rule.DiagnosticFormat,
		FieldPath:        fieldPath,
		FilePath:         fields.FilePathValue(),
		Value:            value,
		Start:            0,
		End:              end,
	}
}

// allConditionsMatch returns true when every condition in the slice matches
// the payload (AND semantics). A condition matches when:
//   - Its Pattern is set and matches the extracted field value, AND
//   - Its NotPattern is either unset or does NOT match the extracted field value.
//
// Empty extracted values are handled only by Pattern and NotPattern, so optional
// fields (for example tool_name when absent) can use not_pattern alone.
func allConditionsMatch(ctx context.Context, fields FieldSet, rule *config.Rule, conditions []config.Condition) bool {
	condCtx := conditionContext{commandCwds: nil}
	if !collectCommandConditionContext(fields, conditions, &condCtx) {
		return false
	}

	for i := range conditions {
		c := &conditions[i]
		switch conditionKind(c) {
		case config.ConditionKindRegex:
			accessor := conditionFieldAccessor{fields: &fields, condition: c}
			if !regexconcern.ConditionMatch(accessor, c) {
				return false
			}
		case config.ConditionKindCommand:
			continue
		case config.ConditionKindProject:
			if !projectConditionMatch(fields, c, condCtx) {
				return false
			}
		case config.ConditionKindDiff:
			if !diffConditionGateMatch(fields, c) {
				return false
			}
		case config.ConditionKindShellWrite:
			if !shellWriteConditionGateMatch(fields, c) {
				return false
			}
		case config.ConditionKindShellRead:
			if !shellReadConditionGateMatch(fields, c) {
				return false
			}
		case config.ConditionKindGitDefaultBranch, config.ConditionKindGitPrimaryCheckout,
			config.ConditionKindGitRefMove:
			if !gitConditionMatch(fields, c, condCtx, gitbranch.ReadState) {
				return false
			}
		case config.ConditionKindExec:
			// Exec runs last in config order because it is the only kind that
			// forks a process; placing it after the cheap conditions means the
			// short-circuit above keeps it from running on non-candidates.
			if !execConditionGateMatch(ctx, fields, rule, i, c) {
				return false
			}
		case config.ConditionKindComposer:
			if !composerConditionGateMatch(ctx, fields, c) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func collectCommandConditionContext(fields FieldSet, conditions []config.Condition, condCtx *conditionContext) bool {
	for i := range conditions {
		c := &conditions[i]
		if conditionKind(c) != config.ConditionKindCommand {
			continue
		}
		cwds, ok := commandConditionCwds(fields, c)
		if !ok {
			return false
		}
		condCtx.commandCwds = append(condCtx.commandCwds, cwds...)
	}
	return true
}

type composerDecider interface {
	Decide(ruleSetID string, command string, cwd string) composer.Verdict
}

type composerDeciderKey struct{}

type defaultComposerDecider struct{}

func (defaultComposerDecider) Decide(ruleSetID string, command string, cwd string) composer.Verdict {
	return composer.Decide(ruleSetID, command, cwd)
}

// WithComposerDecider returns a context carrying decider for composer conditions.
func WithComposerDecider(ctx context.Context, decider composerDecider) context.Context {
	return context.WithValue(ctx, composerDeciderKey{}, decider)
}

func composerDeciderFromContext(ctx context.Context) composerDecider {
	if ctx != nil {
		decider, _ := ctx.Value(composerDeciderKey{}).(composerDecider)
		if decider != nil {
			return decider
		}
	}
	return defaultComposerDecider{}
}

func composerConditionGateMatch(ctx context.Context, fields FieldSet, c *config.Condition) bool {
	command := fields.CommandValue()
	cwd := fields.BaseCWD()
	if command == "" {
		return false
	}
	verdict := composerDeciderFromContext(ctx).Decide(c.RuleSetID, command, cwd)
	return verdict == composer.Block || verdict == composer.Unknown
}

// diffConditionGateMatch reports whether the diff condition would emit any
// match for the current fields. It is used as the per-condition gate inside
// [allConditionsMatch] so the rule fires only when every condition agrees.
func diffConditionGateMatch(fields FieldSet, c *config.Condition) bool {
	if _, ok := safeDiagnosticGroup(c.DiagnosticGroup); !ok {
		return false
	}
	return len(evalDiffCondition(&fields, c, &config.Rule{
		Name:              "",
		Description:       "",
		Events:            nil,
		ClaudeEvents:      nil,
		CursorEvents:      nil,
		CodexEvents:       nil,
		GeminiEvents:      nil,
		Conditions:        nil,
		FieldPaths:        nil,
		Pattern:           "",
		NotPattern:        "",
		Action:            config.ActionBlock,
		ViolationMessage:  "",
		DiagnosticGroup:   0,
		DiagnosticFormat:  config.DiagnosticFormatDetailed,
		RedactDiagnostics: false,
		AuditOnly:         false,
		DisableIfEnv:      nil,
	}, 1)) > 0
}

// safeDiagnosticGroup converts an int diagnostic group value to uint32,
// returning ok=false when the value is outside the representable range.
// The config loader already validates DiagnosticGroup against the regex
// capture count, so this guard is defense-in-depth and lets gosec verify
// the conversion is in range without an inline lint suppression comment.
func safeDiagnosticGroup(group int) (uint32, bool) {
	const maxGroup = 1 << 30
	if group < 0 || group > maxGroup {
		return 0, false
	}
	return uint32(group), true
}

// shellWriteConditionGateMatch reports whether the shell-write condition
// would emit any match for the current fields.
func shellWriteConditionGateMatch(fields FieldSet, c *config.Condition) bool {
	commandSelector := config.FieldToolInputCommand
	for _, sel := range c.Selectors() {
		if sel.Selector != config.FieldSelectorInvalid {
			commandSelector = sel.Selector
			break
		}
	}
	return len(shellwriteconcern.EvalShellWriteMatches(&fields, commandSelector, c.Globs)) > 0
}

// shellReadConditionGateMatch reports whether the shell-read condition would
// emit any match for the current fields.
func shellReadConditionGateMatch(fields FieldSet, c *config.Condition) bool {
	return len(evalShellReadCondition(&fields, c, &config.Rule{
		Name:              "",
		Description:       "",
		Events:            nil,
		ClaudeEvents:      nil,
		CursorEvents:      nil,
		CodexEvents:       nil,
		GeminiEvents:      nil,
		Conditions:        nil,
		FieldPaths:        nil,
		Pattern:           "",
		NotPattern:        "",
		Action:            config.ActionBlock,
		ViolationMessage:  "",
		DiagnosticGroup:   0,
		DiagnosticFormat:  config.DiagnosticFormatDetailed,
		RedactDiagnostics: false,
		AuditOnly:         false,
		DisableIfEnv:      nil,
	}, 1)) > 0
}

type conditionContext struct {
	commandCwds []string
}

func conditionKind(c *config.Condition) config.ConditionKind {
	if c.Kind == "" {
		return config.ConditionKindRegex
	}
	return config.ConditionKind(c.Kind)
}

// CheckedRuleNames returns the names of rules that would be evaluated for
// the given system and event name. Used for audit logging.
func CheckedRuleNames(system, eventName string, rules []config.Rule) []string {
	names := make([]string, 0, len(rules))
	for i := range rules {
		if appliesToEvent(&rules[i], system, eventName) {
			names = append(names, rules[i].Name)
		}
	}
	return names
}

// appliesToEvent returns true when the rule's event filter includes eventName
// for the given system. Only checks the system-specific events array (claude_events
// or cursor_events) plus the shared events array. If all are empty, the rule
// applies to all events.
func appliesToEvent(rule *config.Rule, system, eventName string) bool {
	shared := rule.Events
	specific := systemSpecificEvents(rule, system)

	noFilters := len(shared) == 0 && len(specific) == 0
	if noFilters {
		return true
	}

	return slices.Contains(shared, eventName) ||
		slices.Contains(specific, eventName)
}

type ruleSystem string

const (
	ruleSystemClaude ruleSystem = "claude"
	ruleSystemCodex  ruleSystem = "codex"
	ruleSystemCursor ruleSystem = "cursor"
	ruleSystemGemini ruleSystem = "gemini"
)

func systemSpecificEvents(rule *config.Rule, system string) []string {
	switch ruleSystem(system) {
	case ruleSystemClaude:
		return rule.ClaudeEvents
	case ruleSystemCursor:
		return rule.CursorEvents
	case ruleSystemCodex:
		return rule.CodexEvents
	case ruleSystemGemini:
		return rule.GeminiEvents
	default:
		return nil
	}
}

// cmdChainRe splits a shell command on common chain and sequence operators.
var cmdChainRe = regex.MustCompile(`&&|\|\||;|\n`)

// ReadUserHomeDir returns the current user's home directory.
func ReadUserHomeDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		slog.Warn("read user home dir failed", "error", err)
		return "", fmt.Errorf("read user home dir: %w", err)
	}
	return homeDir, nil
}

// effectiveCwdAfterChain returns the working directory in effect at the end of a
// shell command chain starting in startCwd, computed structurally by
// shelldecomp rather than a cd regex. A leading tilde expands against homeDir.
//
// When shelldecomp cannot pin the final cwd (a cd into an unresolvable target
// such as cd "$VAR"), the result is shelldecomp.Unresolvable: a sentinel that no
// real directory matches, so an index-aware project or read-target check cannot
// scope to a fabricated path. When no cd ran (or the chain left cwd at the
// start), the result is startCwd unchanged.
func effectiveCwdAfterChain(startCwd, homeDir, command string) string {
	decomposition := shelldecomp.Parse(command, startCwd, homeDir)
	cwd := decomposition.EffectiveCwdAt(uint(len(command)))
	if cwd == "" {
		return startCwd
	}
	return cwd
}
