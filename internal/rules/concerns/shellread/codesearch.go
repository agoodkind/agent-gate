package shellread

import (
	"os"

	"goodkind.io/gksyntax/shelldecomp"
)

// ExtractCodeSearchTargets returns the effective filesystem targets of a
// code-search command (the paths it reads), scoped to searchTools, the argv0
// values the calling rule declares as content searchers (for example grep, rg,
// git grep, sed). The tool set is rule policy supplied by config; this package
// carries no built-in list, and an empty set yields no targets. It combines
// two layers:
//
//   - shelldecomp's structural read targets for the declared tools: explicit
//     path operands resolved against the cd-applied cwd, a recursive grep or
//     bare rg/ag/ack with no operand targeting cwd, and a stdin grep (a
//     non-first pipeline stage with no operand) contributing nothing.
//   - the enumerator layer, which covers code search hidden behind an
//     enumerator feeding a declared tool over file contents (find DIR | xargs
//     grep, find DIR -exec grep, git ls-files | xargs rg); shelldecomp does not
//     model that enumerator-to-searcher dataflow, so it is handled here.
//
// A path the command also writes is dropped: a sed -i edits its operand in
// place, so the operand is an edit target, not a content search the semantic
// index could answer. Targets shelldecomp could not pin to a literal absolute
// path (an unexpanded $var operand, a command substitution, or a cd into an
// unresolvable directory) are dropped rather than fabricated, so an
// unresolvable shape stays out of scope.
//
// resolver, when non-nil, lets shelldecomp read an interpreter's script file off
// disk so a python program's real reads are surfaced; a nil resolver leaves the
// behavior identical to the structural-only parse.
func ExtractCodeSearchTargets(command, cwd string, searchTools []string, resolver shelldecomp.FileResolver) []ReadTarget {
	if command == "" || len(searchTools) == 0 {
		return nil
	}
	tools := make(map[string]bool, len(searchTools))
	for _, tool := range searchTools {
		tools[tool] = true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	var out []ReadTarget
	seen := make(map[string]struct{})
	add := func(path string) {
		if _, dup := seen[path]; dup {
			return
		}
		seen[path] = struct{}{}
		out = append(out, ReadTarget{Path: path, Remote: false, Spec: "code-search", Raw: command})
	}
	extractCodeSearchInto(command, cwd, home, tools, add, maxEmbeddedSearchDepth, resolver)
	return out
}

// extractCodeSearchInto extracts one command level into add and recurses into
// embedded code the command would execute locally (a wrapper shell's -c script
// and a heredoc body), bounded by depth. resolver, when non-nil, is threaded
// into shelldecomp so an interpreter's script file is read off disk.
func extractCodeSearchInto(command, cwd, home string, tools map[string]bool, add func(string), depth int, resolver shelldecomp.FileResolver) {
	if depth <= 0 || command == "" {
		return
	}
	decomposition := parseCommand(command, cwd, home, resolver)

	written := make(map[string]struct{})
	for _, target := range decomposition.WriteTargets() {
		written[target.Path] = struct{}{}
	}

	for _, target := range decomposition.ReadTargets() {
		if !tools[target.Argv0] {
			continue
		}
		if _, isWrite := written[target.Path]; isWrite {
			continue
		}
		// shelldecomp guarantees a resolvable target is a pinned absolute path
		// (never a fabricated join of an unexpanded $var), so the old
		// $/backtick belt-and-suspenders filter is no longer needed: an
		// unresolvable operand or an unresolvable cwd already arrives with
		// Resolvable=false and Path==shelldecomp.Unresolvable.
		if !target.Resolvable || target.Path == shelldecomp.Unresolvable {
			continue
		}
		add(target.Path)
	}

	// Enumerator-driven code search (find/fd/git ls-files feeding a declared
	// tool over file contents). shelldecomp surfaces find's own operands as
	// reads, which over- and under-count the enumerated directory, so the
	// enumerator layer computes the real target and the find/fd/git-ls-files
	// reads above are skipped by the declared-tools filter.
	for _, target := range resolvableTargets(enumeratorCodeSearchTargets(command, cwd, tools)) {
		add(target.Path)
	}

	extractEmbeddedCodeSearchInto(decomposition, cwd, home, tools, add, depth, resolver)
}

// parseCommand decomposes a shell command, threading the off-disk resolver
// through shelldecomp when one is supplied. A nil resolver uses the
// structural-only Parse so the no-resolver path stays behavior-identical.
func parseCommand(command, cwd, home string, resolver shelldecomp.FileResolver) *shelldecomp.Decomposition {
	if resolver == nil {
		return shelldecomp.Parse(command, cwd, home)
	}
	return shelldecomp.ParseWithOptions(command, cwd, shelldecomp.Options{Home: home, FileResolver: resolver})
}

// resolvableTargets drops enumerator operands whose path still carries a shell
// expansion (a $variable or `command` substitution) that agent-gate cannot
// evaluate. The enumerator layer resolves directories with the package's own
// resolvePath rather than shelldecomp, so this filter still guards against a
// fabricated $var directory there.
func resolvableTargets(targets []ReadTarget) []ReadTarget {
	out := make([]ReadTarget, 0, len(targets))
	for _, target := range targets {
		if containsShellExpansion(target.Path) {
			continue
		}
		out = append(out, target)
	}
	return out
}

// containsShellExpansion reports whether a path still holds an unresolved shell
// expansion that must not be joined into a fabricated directory.
func containsShellExpansion(path string) bool {
	for _, r := range path {
		if r == '$' || r == '`' {
			return true
		}
	}
	return false
}
