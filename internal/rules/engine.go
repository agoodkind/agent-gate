package rules

import (
	"strings"

	"hookguard/internal/config"
)

// Violation describes a rule that matched the current hook payload.
type Violation struct {
	RuleName string
	Message  string
}

// Evaluate iterates over all rules and returns the first Violation whose
// pattern matches a field extracted from payload, or nil if no rule fires.
//
// eventName is the raw hook_event_name string from the payload.
// payload is the full decoded JSON as a map (same value that was read from stdin).
// rules is the compiled rule list from config.Load().
func Evaluate(eventName string, payload map[string]any, rules []config.Rule) *Violation {
	for i := range rules {
		rule := &rules[i]

		if !appliesToEvent(rule, eventName) {
			continue
		}

		value := extractField(payload, rule.FieldPaths)
		if value == "" {
			// Field not present in this payload; rule does not apply.
			continue
		}

		if rule.Compiled().MatchString(value) {
			return &Violation{
				RuleName: rule.Name,
				Message:  rule.ViolationMessage,
			}
		}
	}
	return nil
}

// CheckedRuleNames returns the names of rules that would be evaluated for
// the given event name. Used for audit logging.
func CheckedRuleNames(eventName string, rules []config.Rule) []string {
	names := make([]string, 0, len(rules))
	for i := range rules {
		if appliesToEvent(&rules[i], eventName) {
			names = append(names, rules[i].Name)
		}
	}
	return names
}

// appliesToEvent returns true when the rule's event filter includes eventName.
// An empty Events slice means the rule applies to all events.
func appliesToEvent(rule *config.Rule, eventName string) bool {
	if len(rule.Events) == 0 {
		return true
	}
	for _, e := range rule.Events {
		if e == eventName {
			return true
		}
	}
	return false
}

// extractField walks each dot-path in paths against the nested map and returns
// the first non-empty string value found. Returns "" if no path resolves.
//
// Example: path "tool_input.command" on {"tool_input": {"command": "ls"}}
// returns "ls".
func extractField(payload map[string]any, paths []string) string {
	for _, path := range paths {
		parts := strings.Split(path, ".")
		if v := navigatePath(payload, parts); v != "" {
			return v
		}
	}
	return ""
}

// navigatePath recursively descends into nested maps following parts.
// Returns the string value at the leaf, or "" if the path does not exist
// or the leaf is not a string.
func navigatePath(node map[string]any, parts []string) string {
	if len(parts) == 0 || node == nil {
		return ""
	}

	val, ok := node[parts[0]]
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
