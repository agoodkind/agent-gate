package shellread

import (
	"os"

	"goodkind.io/gksyntax/shelldecomp"
)

// gitGrepArgv0 is the argv0 label shelldecomp assigns to a `git grep` read
// target. It is excluded from code-search targets because git grep is an
// exact-text search over tracked files that semantic search cannot replace.
const gitGrepArgv0 = "git grep"

// contentSearchers are the argv0 values whose shelldecomp read targets are
// genuine content searches a semantic index could answer. Other readers
// shelldecomp recognizes (cat, head, find, sed) are not code searches: find in
// particular is an enumerator whose code-search contribution is computed by the
// enumerator layer below, not by its own operands.
var contentSearchers = map[string]bool{
	"grep":  true,
	"egrep": true,
	"fgrep": true,
	"rg":    true,
	"ag":    true,
	"ack":   true,
}

// ExtractCodeSearchTargets returns the effective filesystem targets of a
// grep/rg-style command (the paths it reads). It combines two layers:
//
//   - shelldecomp's structural read targets for content searchers (grep, rg,
//     ag, ack): explicit path operands resolved against the cd-applied cwd, a
//     recursive grep or bare rg/ag/ack with no operand targeting cwd, and a
//     stdin grep (a non-first pipeline stage with no operand) contributing
//     nothing.
//   - the enumerator layer, which covers code search hidden behind an
//     enumerator feeding a searcher over file contents (find DIR | xargs grep,
//     find DIR -exec grep, git ls-files | xargs rg); shelldecomp does not model
//     that enumerator-to-searcher dataflow, so it is handled here.
//
// git grep is excluded even though shelldecomp emits it, because it is an
// exact-text search semantic search cannot replace. Targets shelldecomp could
// not pin to a literal absolute path (an unexpanded $var operand, a command
// substitution, or a cd into an unresolvable directory) are dropped rather than
// fabricated, so an unresolvable shape stays out of scope.
func ExtractCodeSearchTargets(command, cwd string) []ReadTarget {
	if command == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	decomposition := shelldecomp.Parse(command, cwd, home)

	var out []ReadTarget
	seen := make(map[string]struct{})
	add := func(path string) {
		if _, dup := seen[path]; dup {
			return
		}
		seen[path] = struct{}{}
		out = append(out, ReadTarget{Path: path, Remote: false, Spec: "code-search", Raw: command})
	}

	for _, target := range decomposition.ReadTargets() {
		if target.Argv0 == gitGrepArgv0 {
			continue
		}
		if !contentSearchers[target.Argv0] {
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

	// Enumerator-driven code search (find/fd/git ls-files feeding a content
	// searcher over file contents). shelldecomp surfaces find's own operands as
	// reads, which over- and under-count the enumerated directory, so the
	// enumerator layer computes the real target and the find/fd/git-ls-files
	// reads above are skipped by the contentSearchers filter.
	for _, target := range resolvableTargets(enumeratorCodeSearchTargets(command, cwd)) {
		add(target.Path)
	}
	return out
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
