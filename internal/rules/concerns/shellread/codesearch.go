package shellread

import (
	"os"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
	"goodkind.io/gksyntax/shelldecomp"
)

type codeSearchPolicy struct {
	tools          map[string]bool
	enumerations   []config.EnumerationSpec
	recursiveGlobs bool
}

// ExtractCodeSearchTargets returns content reads selected by searchTools.
func ExtractCodeSearchTargets(command, cwd string, searchTools []string, resolver shelldecomp.FileResolver) []ReadTarget {
	return ExtractCodeSearchTargetsWithPolicy(command, cwd, searchTools, nil, false, resolver)
}

// ExtractCodeSearchTargetsWithPolicy evaluates independent content and enumeration declarations.
func ExtractCodeSearchTargetsWithPolicy(command, cwd string, searchTools []string, specs []config.EnumerationSpec, recursiveGlobs bool, resolver shelldecomp.FileResolver) []ReadTarget {
	if command == "" || (len(searchTools) == 0 && len(specs) == 0 && !recursiveGlobs) {
		return nil
	}
	policy := codeSearchPolicy{
		tools:          make(map[string]bool, len(searchTools)),
		enumerations:   specs,
		recursiveGlobs: recursiveGlobs,
	}
	for _, tool := range searchTools {
		policy.tools[tool] = true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	var targets []ReadTarget
	seen := make(map[string]struct{})
	add := func(path string) {
		if _, duplicate := seen[path]; duplicate {
			return
		}
		seen[path] = struct{}{}
		targets = append(targets, ReadTarget{Path: path, Remote: false, Spec: "code-search", Raw: command})
	}
	extractCodeSearchInto(command, cwd, home, policy, add, maxEmbeddedSearchDepth, resolver)
	return targets
}

func extractCodeSearchInto(command, cwd, home string, policy codeSearchPolicy, add func(string), depth int, resolver shelldecomp.FileResolver) {
	if depth <= 0 || command == "" {
		return
	}
	command = shellparse.ExpandLiteralAssignments(command)
	decomposition := parseCommand(command, cwd, home, resolver)
	written := make(map[string]struct{})
	for _, target := range decomposition.WriteTargets() {
		written[target.Path] = struct{}{}
	}
	for _, target := range decomposition.ReadTargets() {
		if !policy.tools[target.Argv0] {
			continue
		}
		if _, isWrite := written[target.Path]; isWrite {
			continue
		}
		if !target.Resolvable || target.Path == shelldecomp.Unresolvable {
			continue
		}
		add(target.Path)
	}
	for _, target := range enumerationTargets(command, decomposition, policy.tools, policy.enumerations) {
		add(target.Path)
	}
	if policy.recursiveGlobs {
		for _, parsed := range decomposition.Commands() {
			if parsed.Node == nil || parsed.Cwd == "" || parsed.Cwd == shelldecomp.Unresolvable {
				continue
			}
			for _, target := range resolvableTargets(globTargets(parsed.Node.Text, parsed.Cwd)) {
				add(target.Path)
			}
		}
	}
	extractEmbeddedCodeSearchInto(decomposition, cwd, home, policy, add, depth, resolver)
}

func globTargets(command, cwd string) []ReadTarget {
	var targets []ReadTarget
	for _, directory := range recursiveGlobDirs(command, cwd) {
		targets = append(targets, enumTarget(directory, command))
	}
	return targets
}

func parseCommand(command, cwd, home string, resolver shelldecomp.FileResolver) *shelldecomp.Decomposition {
	if resolver == nil {
		return shelldecomp.Parse(command, cwd, home)
	}
	return shelldecomp.ParseWithOptions(command, cwd, shelldecomp.Options{Home: home, FileResolver: resolver})
}

func resolvableTargets(targets []ReadTarget) []ReadTarget {
	var resolved []ReadTarget
	for _, target := range targets {
		if !containsShellExpansion(target.Path) {
			resolved = append(resolved, target)
		}
	}
	return resolved
}

func containsShellExpansion(path string) bool {
	for _, character := range path {
		if character == '$' || character == '`' {
			return true
		}
	}
	return false
}
