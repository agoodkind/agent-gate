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
	violations := EvaluateAll(ctx, system, eventName, fields, rules)
	if len(violations) == 0 {
		return nil
	}

	return &Violation{
		RuleName:  violations[0].RuleName,
		Message:   violations[0].Message,
		AuditOnly: violations[0].AuditOnly,
	}
}

// EvaluateAll returns every concrete regex match for every applicable rule.
func EvaluateAll(ctx context.Context, system, eventName string, fields FieldSet, rules []config.Rule) []MatchViolation {
	var violations []MatchViolation
	for i := range rules {
		rule := &rules[i]
		if !appliesToEvent(rule, system, eventName) {
			continue
		}
		if len(rule.Conditions) > 0 {
			if allConditionsMatch(fields, rule.Conditions) {
				violations = append(violations, conditionViolations(fields, rule)...)
			}
			continue
		}

		violations = append(violations, fieldViolations(fields, rule, rule.Selectors(), rule.Compiled(), rule.DiagnosticGroup)...)
	}
	return violations
}

func conditionViolations(fields FieldSet, rule *config.Rule) []MatchViolation {
	var violations []MatchViolation
	for i := range rule.Conditions {
		c := &rule.Conditions[i]
		if conditionKind(c) != config.ConditionKindRegex {
			continue
		}
		if c.CompiledPattern() == nil {
			continue
		}
		violations = append(violations, fieldViolations(fields, rule, c.Selectors(), c.CompiledPattern(), c.DiagnosticGroup)...)
	}
	if len(violations) == 0 {
		violations = append(violations, conditionFallbackViolation(fields, rule))
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
	end := len(value)
	if end > 1 {
		end = 1
	}
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

type regexMatcher interface {
	FindAllStringGroupIndex(string, int, uint32) [][2]int
}

func fieldViolations(fields FieldSet, rule *config.Rule, selectors []config.FieldSelectorSpec, re regexMatcher, diagnosticGroup int) []MatchViolation {
	var violations []MatchViolation
	filePath := fields.FilePathValue()
	for _, selector := range selectors {
		value := fields.String(selector.Selector)
		if value == "" {
			continue
		}
		for _, idx := range re.FindAllStringGroupIndex(value, -1, uint32(diagnosticGroup)) {
			violations = append(violations, MatchViolation{
				RuleName:  rule.Name,
				Message:   rule.ViolationMessage,
				AuditOnly: rule.AuditOnly,
				FieldPath: selector.Path,
				FilePath:  filePath,
				Value:     value,
				Start:     idx[0],
				End:       idx[1],
			})
		}
	}
	return violations
}

// allConditionsMatch returns true when every condition in the slice matches
// the payload (AND semantics). A condition matches when:
//   - Its Pattern is set and matches the extracted field value, AND
//   - Its NotPattern is either unset or does NOT match the extracted field value.
//
// Empty extracted values are handled only by Pattern and NotPattern, so optional
// fields (for example tool_name when absent) can use not_pattern alone.
func allConditionsMatch(fields FieldSet, conditions []config.Condition) bool {
	ctx := conditionContext{}
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
			if !regexConditionMatch(fields, c) {
				return false
			}
		case config.ConditionKindCommand:
			continue
		case config.ConditionKindProject:
			if !projectConditionMatch(fields, c, ctx) {
				return false
			}
		default:
			return false
		}
	}
	return true
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

func regexConditionMatch(fields FieldSet, c *config.Condition) bool {
	_, value := extractField(fields, c.Selectors())
	if c.CompiledPattern() != nil && !c.CompiledPattern().MatchString(value) {
		return false
	}
	if c.CompiledNotPattern() != nil && c.CompiledNotPattern().MatchString(value) {
		return false
	}
	return true
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
		if strings.HasPrefix(field, flag+"=") {
			return flag, strings.TrimPrefix(field, flag+"="), true
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
