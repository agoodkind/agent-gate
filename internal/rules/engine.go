package rules

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

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
// pattern matches a field extracted from payload, or nil if no rule fires.
//
// system is "claude", "cursor", or "unknown".
// eventName is the raw hook_event_name string from the payload.
// payload is the full decoded JSON as a map (same value that was read from stdin).
// rules is the compiled rule list from config.Load().
func Evaluate(system, eventName string, payload map[string]any, rules []config.Rule) *Violation {
	violations := EvaluateAll(system, eventName, payload, rules)
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
func EvaluateAll(system, eventName string, payload map[string]any, rules []config.Rule) []MatchViolation {
	var violations []MatchViolation
	for i := range rules {
		rule := &rules[i]
		if !appliesToEvent(rule, system, eventName) {
			continue
		}
		if len(rule.Conditions) > 0 {
			if allConditionsMatch(payload, rule.Conditions) {
				violations = append(violations, conditionViolations(payload, rule)...)
			}
			continue
		}

		violations = append(violations, fieldViolations(payload, rule, rule.FieldPaths, rule.Compiled())...)
	}
	return violations
}

func conditionViolations(payload map[string]any, rule *config.Rule) []MatchViolation {
	var violations []MatchViolation
	for i := range rule.Conditions {
		c := &rule.Conditions[i]
		if c.CompiledPattern() == nil {
			continue
		}
		violations = append(violations, fieldViolations(payload, rule, c.FieldPaths, c.CompiledPattern())...)
	}
	if len(violations) == 0 {
		violations = append(violations, conditionFallbackViolation(payload, rule))
	}
	return violations
}

func conditionFallbackViolation(payload map[string]any, rule *config.Rule) MatchViolation {
	fieldPath := "payload"
	value := rule.Name
	for i := range rule.Conditions {
		for _, path := range rule.Conditions[i].FieldPaths {
			if v := extractField(payload, []string{path}); v != "" {
				fieldPath = path
				value = v
				break
			}
		}
		if fieldPath != "payload" {
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
		FilePath:  payloadFilePath(payload),
		Value:     value,
		Start:     0,
		End:       end,
	}
}

func fieldViolations(payload map[string]any, rule *config.Rule, paths []string, re interface {
	FindAllStringIndex(string, int) [][2]int
}) []MatchViolation {
	var violations []MatchViolation
	filePath := payloadFilePath(payload)
	for _, path := range paths {
		value := extractField(payload, []string{path})
		if value == "" {
			continue
		}
		for _, idx := range re.FindAllStringIndex(value, -1) {
			violations = append(violations, MatchViolation{
				RuleName:  rule.Name,
				Message:   rule.ViolationMessage,
				AuditOnly: rule.AuditOnly,
				FieldPath: path,
				FilePath:  filePath,
				Value:     value,
				Start:     idx[0],
				End:       idx[1],
			})
		}
	}
	return violations
}

func payloadFilePath(payload map[string]any) string {
	for _, path := range []string{"file_path", "path", "tool_input.file_path", "tool_input.path"} {
		if v := extractField(payload, []string{path}); v != "" {
			return v
		}
	}
	return ""
}

// allConditionsMatch returns true when every condition in the slice matches
// the payload (AND semantics). A condition matches when:
//   - Its Pattern is set and matches the extracted field value, AND
//   - Its NotPattern is either unset or does NOT match the extracted field value.
//
// Empty extracted values are handled only by Pattern and NotPattern, so optional
// fields (for example tool_name when absent) can use not_pattern alone.
func allConditionsMatch(payload map[string]any, conditions []config.Condition) bool {
	for i := range conditions {
		c := &conditions[i]
		value := extractField(payload, c.FieldPaths)
		if c.CompiledPattern() != nil && !c.CompiledPattern().MatchString(value) {
			return false
		}
		if c.CompiledNotPattern() != nil && c.CompiledNotPattern().MatchString(value) {
			return false
		}
	}
	return true
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

func systemSpecificEvents(rule *config.Rule, system string) []string {
	switch system {
	case "claude":
		return rule.ClaudeEvents
	case "cursor":
		return rule.CursorEvents
	case "codex":
		return rule.CodexEvents
	case "gemini":
		return rule.GeminiEvents
	default:
		return nil
	}
}

// extractField walks each dot-path in paths against the nested map and returns
// the first non-empty string value found. Returns "" if no path resolves.
//
// Two virtual field names are handled specially:
//
//   - "effective_cwd": simulates cd chains to compute the working directory
//     active when commands execute.
//
//   - "cmd_segments": splits the command on shell chain operators and returns
//     all segments joined by newlines, enabling (?m)^ patterns.
//
// Example: path "tool_input.command" on {"tool_input": {"command": "ls"}}
// returns "ls".
func extractField(payload map[string]any, paths []string) string {
	for _, path := range paths {
		switch path {
		case "effective_cwd":
			if v := effectiveCwd(payload); v != "" {
				return v
			}
		case "cmd_segments":
			if v := cmdSegments(payload); v != "" {
				return v
			}
		default:
			parts := strings.Split(path, ".")
			if v := navigatePath(payload, parts); v != "" {
				return v
			}
		}
	}
	return ""
}

// cmdSegments splits a shell command on chain operators (&&, ||, ;, newline)
// and returns all non-empty segments joined by newlines. Rules can then use
// (?m)^ in their pattern to match against each segment independently.
//
// This is a general-purpose primitive with no knowledge of specific commands.
// The rule author decides what to match within the segments.
//
// Example: "git status && git commit -m msg"
// returns  "git status\ngit commit -m msg"
//
// Example: "git log --grep=\"git commit\""
// returns  "git log --grep=\"git commit\""   (no splitting; no chain operators)
// CmdSegments is exported for direct testing.
func CmdSegments(payload map[string]any) string { return cmdSegments(payload) }

func cmdSegments(payload map[string]any) string {
	cmd := extractField(payload, []string{"tool_input.command", "command"})
	if cmd == "" {
		return ""
	}
	var segs []string
	for _, seg := range cmdChainRe.Split(cmd, -1) {
		seg = strings.TrimSpace(seg)
		if seg != "" {
			segs = append(segs, seg)
		}
	}
	return strings.Join(segs, "\n")
}

// navigatePath recursively descends into nested maps following parts.
// Returns the string value at the leaf, or "" if the path does not exist
// or the leaf is not a string.
//
// A path segment ending in "[*]" (e.g. "edits[*]") selects all elements of
// an array at that key. The remaining sub-path is extracted from each element
// and the results are joined with newlines so that a single MatchString call
// covers every array entry.
func navigatePath(node map[string]any, parts []string) string {
	if len(parts) == 0 || node == nil {
		return ""
	}

	segment := parts[0]

	// Array wildcard: segment like "edits[*]"
	if strings.HasSuffix(segment, "[*]") {
		key := segment[:len(segment)-3]
		val, ok := node[key]
		if !ok {
			return ""
		}
		arr, ok := val.([]any)
		if !ok {
			return ""
		}
		if len(parts) == 1 {
			// Collect string elements directly.
			var collected []string
			for _, elem := range arr {
				if s, ok := elem.(string); ok && s != "" {
					collected = append(collected, s)
				}
			}
			return strings.Join(collected, "\n")
		}
		// Recurse into each array element map with the remaining path.
		var collected []string
		for _, elem := range arr {
			m, ok := elem.(map[string]any)
			if !ok {
				continue
			}
			if v := navigatePath(m, parts[1:]); v != "" {
				collected = append(collected, v)
			}
		}
		return strings.Join(collected, "\n")
	}

	val, ok := node[segment]
	if !ok {
		return ""
	}

	// Leaf node: expect a string.
	if len(parts) == 1 {
		s, _ := val.(string)
		return s
	}

	// Intermediate node: expect a nested map.
	nested, ok := val.(map[string]any)
	if !ok {
		return ""
	}
	return navigatePath(nested, parts[1:])
}

// cmdChainRe splits a shell command on common chain and sequence operators.
var cmdChainRe = regex.MustCompile(`&&|\|\||;|\n`)

// cdCommandRe matches a bare cd command and captures the target path.
// Requires cd at the start of the segment (after trimming whitespace).
var cdCommandRe = regex.MustCompile(`^cd\s+(.+)$`)

// effectiveCwd computes the working directory active when commands in a shell
// chain execute, by simulating cd operations. It starts from the payload cwd
// and applies any cd commands found in the command string in order.
//
// This allows rules to detect the actual working directory at execution time
// rather than just the shell's cwd at hook invocation. For example:
//
//	cwd=/home/user  command="cd /project && git commit"
//	=> effective_cwd="/project"  (correctly not blocked)
//
//	cwd=/home/user  command="git commit"
//	=> effective_cwd="/home/user"  (correctly blocked)
func effectiveCwd(payload map[string]any) string {
	cwd, _ := payload["cwd"].(string)
	if cwd == "" {
		return ""
	}

	// extractField is safe to call here because it only recurses for
	// "effective_cwd" paths, and we pass plain dot-paths below.
	cmd := extractField(payload, []string{"tool_input.command", "command"})
	if cmd == "" {
		return cwd
	}

	home, err := os.UserHomeDir()
	if err != nil {
		home = cwd // best effort
	}

	return ApplyCdChain(cwd, home, cmd)
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

		m := cdCommandRe.FindStringSubmatch(seg)
		if m == nil {
			continue
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
			cwd = homeDir
		case strings.HasPrefix(target, "~/"):
			cwd = filepath.Join(homeDir, target[2:])
		case filepath.IsAbs(target):
			cwd = target
		default:
			cwd = filepath.Join(cwd, target)
		}
	}

	return cwd
}
