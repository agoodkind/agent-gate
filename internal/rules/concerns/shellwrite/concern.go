package shellwrite

import (
	"path/filepath"

	"goodkind.io/agent-gate/internal/config"
)

// FieldAccessor is the subset of the rules.FieldSet view used by this
// Concern. It is exported so callers can pass any compatible accessor
// without importing the rules package and creating an import cycle.
type FieldAccessor interface {
	String(selector config.FieldSelector) string
	FilePathValue() string
	BaseCWD() string
}

// MatchResult records one write target that matched the rule's globs (or
// one sentinel target when the rule chooses to default-deny unparseable
// shapes).
type MatchResult struct {
	FieldPath string
	FilePath  string
	Value     string
	Start     int
	End       int
	Tool      string
	Reason    string
}

// EvalShellWriteMatches parses the command field, extracts every write
// target, and returns one [MatchResult] per target whose path matches any
// of the supplied globs. Unparseable shapes (eval, command substitution,
// bash -c) are returned regardless of globs so the rule can default-deny
// them.
//
// commandSelector defaults to [config.FieldToolInputCommand] when the zero
// value is passed; this matches the canonical Claude/Cursor PreToolUse
// payload shape.
func EvalShellWriteMatches(fields FieldAccessor, commandSelector config.FieldSelector, globs []string) []MatchResult {
	if commandSelector == config.FieldSelectorInvalid {
		commandSelector = config.FieldToolInputCommand
	}
	command := fields.String(commandSelector)
	if command == "" {
		command = fields.String(config.FieldCommand)
	}
	if command == "" {
		return nil
	}
	cwd := fields.BaseCWD()
	targets := ExtractWriteTargets(command, cwd)
	if len(targets) == 0 {
		return nil
	}
	filePath := fields.FilePathValue()
	var out []MatchResult
	for _, target := range targets {
		if target.Reason == ReasonUnparsedCommandShape {
			out = append(out, MatchResult{
				FieldPath: "tool_input.command",
				FilePath:  filePath,
				Value:     command,
				Start:     0,
				End:       minLen(command, 1),
				Tool:      target.Tool,
				Reason:    target.Reason,
			})
			continue
		}
		if !pathMatchesAnyGlob(target.Path, globs) {
			continue
		}
		out = append(out, MatchResult{
			FieldPath: "tool_input.command",
			FilePath:  filePath,
			Value:     command,
			Start:     0,
			End:       minLen(command, 1),
			Tool:      target.Tool,
			Reason:    target.Reason,
		})
	}
	return out
}

func pathMatchesAnyGlob(path string, globs []string) bool {
	if len(globs) == 0 {
		return false
	}
	base := filepath.Base(path)
	for _, glob := range globs {
		if matched, err := filepath.Match(glob, path); err == nil && matched {
			return true
		}
		if matched, err := filepath.Match(glob, base); err == nil && matched {
			return true
		}
	}
	return false
}

func minLen(s string, want int) int {
	if len(s) < want {
		return len(s)
	}
	return want
}
