package rules

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/regex"
	diffconcern "goodkind.io/agent-gate/internal/rules/concerns/diff"
	regexconcern "goodkind.io/agent-gate/internal/rules/concerns/regex"
	shellwriteconcern "goodkind.io/agent-gate/internal/rules/concerns/shellwrite"
	"goodkind.io/agent-gate/pipeline"
)

// Violation describes a rule that matched the current hook payload.
type Violation struct {
	RuleName  string
	Message   string
	AuditOnly bool
}

// MatchViolation describes one concrete regex match that violated a rule.
type MatchViolation struct {
	RuleName  string
	Message   string
	AuditOnly bool
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
}

// Evaluate iterates over all rules and returns the first Violation whose
// pattern matches a typed field selected from payload, or nil if no rule fires.
func Evaluate(ctx context.Context, system, eventName string, fields FieldSet, rules []config.Rule) *Violation {
	violations := EvaluateAll(ctx, system, eventName, fields, rules, nil)
	if len(violations) == 0 {
		return nil
	}

	return &Violation{
		RuleName:  violations[0].RuleName,
		Message:   violations[0].Message,
		AuditOnly: violations[0].AuditOnly,
	}
}

// EvaluateAll returns every concrete match for every applicable rule across
// all condition kinds. One pipeline.Concern is built per applicable regex
// unit: simple rules get one Concern each; condition-based rules get one
// Concern per regex/diff/shell-write-kind condition. Command and project
// conditions remain inline (landings 6 and 7 will migrate those).
//
// getenv is consulted by the [config.Rule.DisableIfEnv] guard. Pass nil to
// disable env-based rule skipping.
func EvaluateAll(ctx context.Context, system, eventName string, fields FieldSet, rulesSlice []config.Rule, getenv func(string) string) []MatchViolation {
	concerns := buildRuleRegexConcerns(&fields, rulesSlice, system, eventName, getenv)
	if len(concerns) == 0 {
		return nil
	}
	orch := &pipeline.Orchestrator{
		Concerns:  concerns,
		Scheduler: pipeline.FixedScheduler{SlotCount: 1},
		Sentinel:  pipeline.NoopSentinel{},
	}
	results, _ := orch.Run(ctx, nil)
	return aggregateResults(results, fields)
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

// aggregateResults flattens Orchestrator results into MatchViolation values.
// For condition-based rules where the gate fired but all regex conditions produced
// zero matches, one fallback violation is emitted per rule.
func aggregateResults(results []pipeline.Result, fields FieldSet) []MatchViolation {
	// Track which condition-based rules fired but produced no regex matches.
	// Key is rule pointer (same slice element across concerns for one rule).
	type ruleState struct {
		rule        *config.Rule
		matchedGate bool
		matchCount  int
	}
	var gateRules []*ruleState
	ruleStateByPtr := map[*config.Rule]*ruleState{}

	var violations []MatchViolation
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
	return violations
}

// buildRuleRegexConcerns builds one Concern per evaluation unit.
// Simple rules contribute one Concern each.
// Condition-based rules with at least one matching condition contribute one
// Concern per regex/diff/shell-write-kind condition.
// Condition-based rules with only non-matching conditions (command, project)
// contribute one Concern that checks the gate and returns the fallback
// violation on match.
// Rules whose [config.Rule.DisableIfEnv] guard fires are skipped.
// Non-applicable rules are skipped.
// fields is passed by pointer so all Concerns share the single EvaluateAll-owned copy.
func buildRuleRegexConcerns(fields *FieldSet, rulesSlice []config.Rule, system, eventName string, getenv func(string) string) []pipeline.Concern {
	var concerns []pipeline.Concern
	for i := range rulesSlice {
		rule := &rulesSlice[i]
		if !appliesToEvent(rule, system, eventName) {
			continue
		}
		if envGuardFires(getenv, rule.DisableIfEnv) {
			continue
		}
		if len(rule.Conditions) == 0 {
			concerns = append(concerns, &ruleRegexConcern{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: -1,
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
			concerns = append(concerns, &ruleRegexConcern{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: j,
			})
			matchingCondCount++
		}

		if matchingCondCount == 0 {
			// Rule has only gate-only conditions (command, project). One
			// fallback Concern represents it.
			concerns = append(concerns, &ruleRegexConcern{
				name:    rule.Name,
				fields:  fields,
				rule:    rule,
				condIdx: -2,
			})
		}
	}
	return concerns
}

// isMatchingConditionKind reports whether c is a kind that produces concrete
// match spans (regex, diff, shell-write) rather than a gate-only kind
// (command, project) handled by [allConditionsMatch] alone. The exhaustive
// switch ensures any new kind added to [config.ConditionKind] forces an
// explicit decision here.
func isMatchingConditionKind(c *config.Condition) bool {
	switch conditionKind(c) {
	case config.ConditionKindRegex, config.ConditionKindDiff, config.ConditionKindShellWrite:
		return true
	case config.ConditionKindCommand, config.ConditionKindProject:
		// Gate-only kinds. They must pass for the rule to fire but they do
		// not by themselves emit per-match diagnostics, so they are evaluated
		// as part of the gate inside [allConditionsMatch] and surfaced via
		// the fallback Concern (condIdx == -2) rather than a per-condition
		// Concern.
		return false
	default:
		return false
	}
}

// ruleRegexConcern is a pipeline.Concern for one regex evaluation unit.
// condIdx == -1 means this is a simple rule evaluated against its top-level pattern.
// condIdx >= 0 means this is one regex-kind condition of a condition-based rule.
// fields is a pointer to the EvaluateAll-owned copy to avoid N redundant copies.
type ruleRegexConcern struct {
	name    string
	fields  *FieldSet
	rule    *config.Rule
	condIdx int
}

// ruleOutcome carries the violations produced by one ruleRegexConcern plus the
// metadata that aggregateResults needs to deduplicate fallback generation.
type ruleOutcome struct {
	violations       []MatchViolation
	rule             *config.Rule
	isConditionBased bool
	gateMatched      bool
}

func (r *ruleRegexConcern) Profile() pipeline.Profile {
	return pipeline.Profile{
		Name:         r.name,
		Cost:         pipeline.CostCheap,
		Idempotent:   true,
		MemoLifetime: pipeline.MemoEvent,
	}
}

func (r *ruleRegexConcern) Execute(_ context.Context, _ pipeline.Input) (pipeline.Outcome, error) {
	if r.condIdx == -1 {
		return ruleOutcome{
			violations:       evalSimpleRule(r.fields, r.rule),
			rule:             nil,
			isConditionBased: false,
			gateMatched:      false,
		}, nil
	}
	// condIdx == -2: rule whose conditions are all non-regex (command, project).
	// The fallback violation is returned inline when the gate passes; aggregateResults
	// sees matchCount > 0 and does not add another fallback.
	if r.condIdx == -2 {
		if !allConditionsMatch(*r.fields, r.rule.Conditions) {
			return ruleOutcome{
				violations:       nil,
				rule:             r.rule,
				isConditionBased: true,
				gateMatched:      false,
			}, nil
		}
		return ruleOutcome{
			violations:       []MatchViolation{conditionFallbackViolation(*r.fields, r.rule)},
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	}
	// condIdx >= 0: one matching-kind condition. The full gate must pass before
	// matches are evaluated. aggregateResults adds a single fallback when all
	// matching conditions for the rule produce zero violations in aggregate.
	if !allConditionsMatch(*r.fields, r.rule.Conditions) {
		return ruleOutcome{
			violations:       nil,
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      false,
		}, nil
	}
	c := &r.rule.Conditions[r.condIdx]
	switch conditionKind(c) {
	case config.ConditionKindRegex:
		matches := regexconcern.EvalFieldMatches(r.fields, c.Selectors(), c.CompiledPattern(), c.DiagnosticGroup)
		return ruleOutcome{
			violations:       matchesToViolations(matches, r.rule),
			rule:             r.rule,
			isConditionBased: true,
			gateMatched:      true,
		}, nil
	case config.ConditionKindDiff:
		return ruleOutcome{
			violations:       evalDiffCondition(r.fields, c, r.rule),
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
	case config.ConditionKindCommand, config.ConditionKindProject:
		// Gate-only kinds are handled by [allConditionsMatch] above and
		// produce no per-condition Concern. Reaching this arm means
		// [buildRuleRegexConcerns] mis-routed a condition; emit nothing
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

// evalDiffCondition runs the diff Concern for one condition and returns its
// match violations in the rules.MatchViolation shape.
func evalDiffCondition(fields *FieldSet, c *config.Condition, rule *config.Rule) []MatchViolation {
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
		matches = append(matches, diffconcern.EvalDiffMatches(fields, pair, pattern, group)...)
		// Fallback: when the configured new field is empty but edits[*] has
		// content, apply the same pattern against the joined edits view so
		// a single condition covers both Edit and MultiEdit shapes.
		if len(matches) == 0 && pair.NewField == config.FieldToolInputNewString {
			batchPair := config.FieldPairSpec{
				OldPath:  "edits[*].old_string",
				NewPath:  "edits[*].new_string",
				OldField: config.FieldEditsOldString,
				NewField: config.FieldEditsNewString,
			}
			matches = append(matches, diffconcern.EvalDiffMatches(fields, batchPair, pattern, group)...)
		}
	}
	return diffMatchesToViolations(matches, rule)
}

func diffMatchesToViolations(matches []diffconcern.MatchResult, rule *config.Rule) []MatchViolation {
	if len(matches) == 0 {
		return nil
	}
	out := make([]MatchViolation, len(matches))
	for i, m := range matches {
		out[i] = MatchViolation{
			RuleName:  rule.Name,
			Message:   rule.ViolationMessage,
			AuditOnly: rule.AuditOnly,
			FieldPath: m.FieldPath,
			FilePath:  m.FilePath,
			Value:     m.Value,
			Start:     m.Start,
			End:       m.End,
		}
	}
	return out
}

// evalShellWriteCondition runs the shellwrite Concern for one condition and
// returns its match violations in the rules.MatchViolation shape.
func evalShellWriteCondition(fields *FieldSet, c *config.Condition, rule *config.Rule) []MatchViolation {
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
	out := make([]MatchViolation, len(matches))
	for i, m := range matches {
		out[i] = MatchViolation{
			RuleName:  rule.Name,
			Message:   rule.ViolationMessage,
			AuditOnly: rule.AuditOnly,
			FieldPath: m.FieldPath,
			FilePath:  m.FilePath,
			Value:     m.Value,
			Start:     m.Start,
			End:       m.End,
		}
	}
	return out
}

// evalSimpleRule runs the top-level pattern for a rule with no conditions.
func evalSimpleRule(fields *FieldSet, rule *config.Rule) []MatchViolation {
	matches := regexconcern.EvalFieldMatches(fields, rule.Selectors(), rule.Compiled(), rule.DiagnosticGroup)
	return matchesToViolations(matches, rule)
}

// matchesToViolations converts MatchResult values from the concern package into
// MatchViolation values with rule metadata attached.
func matchesToViolations(matches []regexconcern.MatchResult, rule *config.Rule) []MatchViolation {
	if len(matches) == 0 {
		return nil
	}
	violations := make([]MatchViolation, len(matches))
	for i, m := range matches {
		violations[i] = MatchViolation{
			RuleName:  rule.Name,
			Message:   rule.ViolationMessage,
			AuditOnly: rule.AuditOnly,
			FieldPath: m.FieldPath,
			FilePath:  m.FilePath,
			Value:     m.Value,
			Start:     m.Start,
			End:       m.End,
		}
	}
	return violations
}

func conditionFallbackViolation(fields FieldSet, rule *config.Rule) MatchViolation {
	fieldPath := "payload"
	value := rule.Name
	for i := range rule.Conditions {
		if path, extracted := extractField(fields, rule.Conditions[i].Selectors()); extracted != "" {
			fieldPath = path
			value = extracted
			break
		}
	}
	end := min(len(value), 1)
	return MatchViolation{
		RuleName:  rule.Name,
		Message:   rule.ViolationMessage,
		AuditOnly: rule.AuditOnly,
		FieldPath: fieldPath,
		FilePath:  fields.FilePathValue(),
		Value:     value,
		Start:     0,
		End:       end,
	}
}

// allConditionsMatch returns true when every condition in the slice matches
// the payload (AND semantics). A condition matches when:
//   - Its Pattern is set and matches the extracted field value, AND
//   - Its NotPattern is either unset or does NOT match the extracted field value.
//
// Empty extracted values are handled only by Pattern and NotPattern, so optional
// fields (for example tool_name when absent) can use not_pattern alone.
func allConditionsMatch(fields FieldSet, conditions []config.Condition) bool {
	ctx := conditionContext{commandCwds: nil}
	for i := range conditions {
		c := &conditions[i]
		if conditionKind(c) != config.ConditionKindCommand {
			continue
		}
		cwds, ok := commandConditionCwds(fields, c)
		if !ok {
			return false
		}
		ctx.commandCwds = append(ctx.commandCwds, cwds...)
	}

	for i := range conditions {
		c := &conditions[i]
		switch conditionKind(c) {
		case config.ConditionKindRegex:
			if !regexconcern.ConditionMatch(fields, c) {
				return false
			}
		case config.ConditionKindCommand:
			continue
		case config.ConditionKindProject:
			if !projectConditionMatch(fields, c, ctx) {
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
		default:
			return false
		}
	}
	return true
}

// diffConditionGateMatch reports whether the diff condition would emit any
// match for the current fields. It is used as the per-condition gate inside
// [allConditionsMatch] so the rule fires only when every condition agrees.
func diffConditionGateMatch(fields FieldSet, c *config.Condition) bool {
	if _, ok := safeDiagnosticGroup(c.DiagnosticGroup); !ok {
		return false
	}
	return len(evalDiffCondition(&fields, c, &config.Rule{
		Name:             "",
		Description:      "",
		Events:           nil,
		ClaudeEvents:     nil,
		CursorEvents:     nil,
		CodexEvents:      nil,
		GeminiEvents:     nil,
		Conditions:       nil,
		FieldPaths:       nil,
		Pattern:          "",
		Action:           "",
		ViolationMessage: "",
		DiagnosticGroup:  0,
		AuditOnly:        false,
		DisableIfEnv:     nil,
	})) > 0
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

type conditionContext struct {
	commandCwds []string
}

func conditionKind(c *config.Condition) config.ConditionKind {
	if c.Kind == "" {
		return config.ConditionKindRegex
	}
	return config.ConditionKind(c.Kind)
}

func commandConditionCwds(fields FieldSet, c *config.Condition) ([]string, bool) {
	var matches []string
	for _, segment := range commandSegmentsWithCwd(fields) {
		fields := shellFields(segment.command)
		if c.StripEnv {
			fields = trimEnvAssignments(fields)
		}
		fields, cwd := normalizeCommandFields(fields, segment.cwd, c)
		if len(fields) == 0 {
			continue
		}

		argv0 := filepath.Base(fields[0])
		if c.Argv0 != "" && argv0 != c.Argv0 {
			continue
		}
		if len(c.Subcommands) == 0 {
			if !conditionTextMatch(strings.Join(fields[1:], " "), c) {
				continue
			}
			matches = append(matches, cwd)
			continue
		}
		if len(fields) > 1 && slices.Contains(c.Subcommands, fields[1]) {
			if !commandTailMatch(fields, c) {
				continue
			}
			matches = append(matches, cwd)
		}
	}

	return matches, len(matches) > 0
}

func commandTailMatch(fields []string, c *config.Condition) bool {
	if len(fields) < 2 {
		return conditionTextMatch("", c)
	}
	return conditionTextMatch(strings.Join(fields[1:], " "), c)
}

func conditionTextMatch(value string, c *config.Condition) bool {
	if re := c.CompiledPattern(); re != nil {
		if !re.MatchString(value) {
			return false
		}
	} else if c.Pattern != "" {
		re, err := regex.Compile(c.Pattern)
		if err != nil || !re.MatchString(value) {
			return false
		}
	}

	if re := c.CompiledNotPattern(); re != nil {
		if re.MatchString(value) {
			return false
		}
	} else if c.NotPattern != "" {
		re, err := regex.Compile(c.NotPattern)
		if err != nil || re.MatchString(value) {
			return false
		}
	}

	return true
}

func normalizeCommandFields(fields []string, cwd string, c *config.Condition) ([]string, string) {
	fields, cwd = applyCwdFlags(fields, cwd, c.CwdFlags)
	for len(fields) > 0 && slices.Contains(c.StripArgs, filepath.Base(fields[0])) {
		fields = fields[1:]
		if c.StripEnv {
			fields = trimEnvAssignments(fields)
		}
		fields, cwd = applyCwdFlags(fields, cwd, c.CwdFlags)
	}
	return fields, cwd
}

func applyCwdFlags(fields []string, cwd string, flags []string) ([]string, string) {
	if len(fields) == 0 || len(flags) == 0 {
		return fields, cwd
	}

	out := make([]string, 0, len(fields))
	out = append(out, fields[0])
	for i := 1; i < len(fields); i++ {
		field := fields[i]
		if _, value, ok := splitCwdFlag(field, flags); ok {
			if value == "" && i+1 < len(fields) {
				value = fields[i+1]
				i++
			}
			if value != "" {
				cwd = resolvePath(cwd, value)
			}
			continue
		}
		out = append(out, field)
	}
	return out, cwd
}

func splitCwdFlag(field string, flags []string) (string, string, bool) {
	for _, flag := range flags {
		if field == flag {
			return flag, "", true
		}
		if after, ok := strings.CutPrefix(field, flag+"="); ok {
			return flag, after, true
		}
	}
	return "", "", false
}

func resolvePath(cwd, path string) string {
	switch {
	case path == "~":
		home, err := os.UserHomeDir()
		if err != nil {
			return cwd
		}
		return home
	case strings.HasPrefix(path, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return cwd
		}
		return filepath.Join(home, path[2:])
	case filepath.IsAbs(path):
		return path
	default:
		return filepath.Join(cwd, path)
	}
}

func projectConditionMatch(fields FieldSet, c *config.Condition, ctx conditionContext) bool {
	cwds := ctx.commandCwds
	if len(cwds) == 0 {
		if cwd := fields.String(config.FieldEffectiveCWD); cwd != "" {
			cwds = []string{cwd}
		}
	}
	if len(cwds) == 0 {
		return false
	}

	for _, cwd := range cwds {
		if projectConditionMatchCwd(cwd, c) {
			return true
		}
	}
	return false
}

func projectConditionMatchCwd(cwd string, c *config.Condition) bool {
	root := cwd
	if len(c.RootMarkers) > 0 {
		found, ok := findProjectRoot(cwd, c.RootMarkers)
		if !ok {
			return false
		}
		root = found
	}

	if len(c.RequireAny) > 0 && !anyPathExists(root, c.RequireAny) {
		return false
	}
	if len(c.RequireAll) > 0 && !allPathsExist(root, c.RequireAll) {
		return false
	}
	if len(c.ForbidAny) > 0 && anyPathExists(root, c.ForbidAny) {
		return false
	}

	return true
}

type commandSegment struct {
	command string
	cwd     string
}

func commandSegmentsWithCwd(fields FieldSet) []commandSegment {
	cwd := fields.BaseCWD()
	if cwd == "" {
		return nil
	}

	cmd := fields.CommandValue()
	if cmd == "" {
		return nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = cwd
	}

	cmd = stripHeredocBodies(cmd)

	var out []commandSegment
	for _, seg := range cmdChainRe.Split(cmd, -1) {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		out = append(out, commandSegment{command: seg, cwd: cwd})
		if next, ok := cdTarget(cwd, home, seg); ok {
			cwd = next
		}
	}
	return out
}

func stripHeredocBodies(command string) string {
	lines := strings.Split(command, "\n")
	var out []string
	var pending []string

	for _, line := range lines {
		if len(pending) > 0 {
			if strings.TrimSpace(line) == pending[0] {
				pending = pending[1:]
			}
			continue
		}

		out = append(out, line)
		pending = append(pending, heredocDelimiters(line)...)
	}

	return strings.Join(out, "\n")
}

func heredocDelimiters(line string) []string {
	fields := shellFields(line)
	var out []string
	for i := 0; i < len(fields); i++ {
		field := fields[i]
		switch {
		case field == "<<" || field == "<<-":
			if i+1 < len(fields) {
				out = append(out, fields[i+1])
				i++
			}
		case strings.HasPrefix(field, "<<-") && len(field) > len("<<-"):
			out = append(out, strings.TrimPrefix(field, "<<-"))
		case strings.HasPrefix(field, "<<") && len(field) > len("<<"):
			out = append(out, strings.TrimPrefix(field, "<<"))
		}
	}
	return out
}

func findProjectRoot(start string, markers []string) (string, bool) {
	dir := filepath.Clean(start)
	for {
		if anyPathExists(dir, markers) {
			return dir, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func anyPathExists(root string, paths []string) bool {
	for _, path := range paths {
		if pathExists(filepath.Join(root, path)) {
			return true
		}
	}
	return false
}

func allPathsExist(root string, paths []string) bool {
	for _, path := range paths {
		if !pathExists(filepath.Join(root, path)) {
			return false
		}
	}
	return true
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func trimEnvAssignments(fields []string) []string {
	for len(fields) > 0 && isEnvAssignment(fields[0]) {
		fields = fields[1:]
	}
	return fields
}

func isEnvAssignment(s string) bool {
	i := strings.IndexByte(s, '=')
	if i <= 0 {
		return false
	}
	for j, r := range s[:i] {
		if j == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func shellFields(s string) []string {
	var fields []string
	var b strings.Builder
	var quote rune
	escaped := false

	flush := func() {
		if b.Len() == 0 {
			return
		}
		fields = append(fields, b.String())
		b.Reset()
	}

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			b.WriteRune(r)
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			b.WriteRune(r)
		}
	}
	flush()
	return fields
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

// extractField returns the first non-empty value selected by a compiled field selector.
func extractField(fields FieldSet, selectors []config.FieldSelectorSpec) (string, string) {
	return fields.FirstString(selectors)
}

// cmdChainRe splits a shell command on common chain and sequence operators.
var cmdChainRe = regex.MustCompile(`&&|\|\||;|\n`)

// cdCommandRe matches a bare cd command and captures the target path.
// Requires cd at the start of the segment (after trimming whitespace).
var cdCommandRe = regex.MustCompile(`^cd\s+(.+)$`)

func osUserHomeDir() (string, error) {
	return os.UserHomeDir()
}

// ApplyCdChain walks the segments of a shell command chain in order, applying
// each cd operation to the running directory, and returns the final effective
// cwd. Non-cd segments are skipped (they do not change the working directory).
//
// Handles:
//   - Absolute paths:          cd /some/path
//   - Home-relative paths:     cd ~/path   or   cd ~
//   - Relative paths:          cd ../sibling
//   - Single/double-quoted:    cd "/path with spaces"
//
// Exported so that it can be tested directly.
func ApplyCdChain(startCwd, homeDir, command string) string {
	segments := cmdChainRe.Split(command, -1)
	cwd := startCwd

	for _, seg := range segments {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}

		if next, ok := cdTarget(cwd, homeDir, seg); ok {
			cwd = next
		}
	}

	return cwd
}

func cdTarget(cwd, homeDir, segment string) (string, bool) {
	m := cdCommandRe.FindStringSubmatch(segment)
	if m == nil {
		return "", false
	}

	target := strings.TrimSpace(m[1])
	// Strip surrounding matching quotes.
	if len(target) >= 2 {
		if (target[0] == '"' && target[len(target)-1] == '"') ||
			(target[0] == '\'' && target[len(target)-1] == '\'') {
			target = target[1 : len(target)-1]
		}
	}

	switch {
	case target == "~":
		return homeDir, true
	case strings.HasPrefix(target, "~/"):
		return filepath.Join(homeDir, target[2:]), true
	case filepath.IsAbs(target):
		return target, true
	default:
		return filepath.Join(cwd, target), true
	}
}
