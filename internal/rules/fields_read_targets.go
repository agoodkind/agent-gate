package rules

import (
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellread"
	"goodkind.io/gksyntax/shelldecomp"
)

// CmdReadTargets returns the newline-joined effective filesystem targets of a
// command (the paths it reads), from two sources a rule declares itself.
//
// search_tools names the argv0 values shelldecomp should treat as content
// readers. That covers every command shelldecomp already classifies as a
// reading kind, and it is the source that understands cd chains, pipelines,
// and interpreter bodies.
//
// read_specs names a command shape positionally: which argv0, where its path
// operands start, and how many leading positionals to skip. It reaches commands
// shelldecomp does not classify as readers at all, where search_tools alone
// yields nothing however the tool is declared. `diff a b` is the case that
// motivated wiring it here: shelldecomp gives diff no command kind, so
// declaring "diff" in search_tools resolves no target, while a read_spec with
// path_arg_start = 1 resolves both operands.
//
// Both lists are rule policy with no built-in default. With neither declared
// there are no targets.
//
// The base (pre-cd) working directory is passed to ExtractCodeSearchTargets,
// which decomposes the whole command with shelldecomp and applies the cd chain
// itself, so `cd /other && grep -rn X .` is attributed to /other rather than the
// session cwd, and an unresolvable `cd "$VAR" && grep -rn X .` yields no
// resolvable target (shelldecomp cannot pin the cwd, so the operand is dropped
// rather than fabricated). Passing the base cwd, not effectiveCWD(), avoids
// applying the cd chain twice.
func (fields FieldSet) CmdReadTargets(
	searchTools []string,
	readSpecs []config.ShellReadSpec,
	resolver shelldecomp.FileResolver,
) string {
	if len(searchTools) == 0 && len(readSpecs) == 0 {
		return ""
	}
	if !fields.hasShellCommandContext() {
		return ""
	}
	command := fields.CommandValue()
	if command == "" {
		return ""
	}

	baseCWD := fields.BaseCWD()
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	collect := func(targets []shellread.ReadTarget) {
		for _, target := range targets {
			if target.Remote || target.Path == "" {
				continue
			}
			if _, duplicate := seen[target.Path]; duplicate {
				continue
			}
			seen[target.Path] = struct{}{}
			paths = append(paths, target.Path)
		}
	}

	if len(searchTools) > 0 {
		collect(shellread.ExtractCodeSearchTargets(command, baseCWD, searchTools, resolver))
	}
	if len(readSpecs) > 0 {
		collect(shellread.ExtractReadTargets(command, baseCWD, readSpecs))
	}
	return strings.Join(paths, "\n")
}

// ExecTargets returns the best target key for an exec validator cache entry:
// resolved read targets first, then the hook file path, then the effective
// working directory. This keeps Grep-style validators target-aware while
// preserving the old cwd fallback for payloads without a concrete file.
func (fields FieldSet) ExecTargets(
	searchTools []string,
	readSpecs []config.ShellReadSpec,
	resolver shelldecomp.FileResolver,
) string {
	if targets := fields.CmdReadTargets(searchTools, readSpecs, resolver); targets != "" {
		return targets
	}
	if path := fields.FilePathValue(); path != "" {
		return path
	}
	if cwd := fields.effectiveCWD(); cwd != "" && cwd != shelldecomp.Unresolvable {
		return cwd
	}
	return fields.BaseCWD()
}
