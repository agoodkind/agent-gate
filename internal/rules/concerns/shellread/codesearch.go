package shellread

import (
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
)

// recurseByDefaultTools scan the working tree when given no path operand.
var recurseByDefaultTools = []string{"rg", "ripgrep", "ag"}

// grepTools take a path operand and only recurse when asked.
var grepTools = []string{"grep", "egrep", "fgrep", "rgrep"}

// codeSearchSpecs parse the positional path operands of code-search tools. The
// first positional is the search pattern (SkipPositionals: 1); when the pattern
// is supplied through -e/-f instead, that flag value consumes the positional
// budget so the first real operand is still treated as a path. Flag values that
// are not paths (context counts, globs, include/exclude filters) are skipped.
var codeSearchSpecs = []config.ShellReadSpec{
	{
		Name:                     "code-search",
		Argv0:                    []string{"grep", "egrep", "fgrep", "rgrep", "rg", "ripgrep", "ag"},
		PathArgStart:             1,
		PathArgStartIfFlags:      nil,
		PathArgStartIfFlagsValue: 0,
		SkipPositionals:          1,
		SkipFlagsWithValues: []string{
			"-e", "--regexp", "-f", "--file",
			"-m", "--max-count",
			"-A", "--after-context", "-B", "--before-context", "-C", "--context",
			"-g", "--glob", "--include", "--exclude", "--exclude-dir",
		},
		SkipFlagValuesAsPositionals: []string{"-e", "--regexp", "-f", "--file"},
		NestedCommand:               false,
		NestedCommandFlag:           "",
		NestedRemote:                false,
		RemoteSources:               false,
	},
}

// ExtractCodeSearchTargets returns the effective filesystem targets of a
// grep/rg-style command. Explicit path operands win. When a code-search tool
// searches the working tree by default (recursive grep, or bare rg/ag) and
// names no path, the effective target is cwd. A tool reading stdin (a plain
// grep with no path) has no target and returns nil, so the caller treats it as
// out of scope. git grep is deliberately excluded: it is an exact-text search
// semantic search cannot replace.
func ExtractCodeSearchTargets(command, cwd string) []ReadTarget {
	operands := ExtractReadTargets(command, cwd, codeSearchSpecs)
	if len(operands) > 0 {
		// The command named explicit operands. Keep the ones we can resolve and
		// drop those carrying a shell expansion we cannot evaluate; an
		// unexpanded $var joined to cwd would falsely look like a repo path. If
		// every operand is unresolvable the command stays out of scope rather
		// than falling back to the working tree.
		return resolvableTargets(operands)
	}
	if cwd != "" && commandSearchesWorkingTree(command) {
		return []ReadTarget{{Path: cwd, Remote: false, Spec: "code-search-cwd", Raw: command}}
	}
	// No operand and no bare working-tree search. A code search can still hide
	// behind an enumerator that feeds a searcher (find DIR | xargs grep,
	// find DIR -exec grep, git ls-files | xargs rg) or a bare code-file
	// enumeration (find DIR -name '*.go'); the enumerated directory is the
	// effective target. Drop unresolvable $var/backtick directories as above.
	return resolvableTargets(enumeratorCodeSearchTargets(command, cwd))
}

// resolvableTargets drops operands whose path still contains a shell expansion
// (a $variable or `command` substitution) that agent-gate cannot evaluate.
func resolvableTargets(targets []ReadTarget) []ReadTarget {
	out := make([]ReadTarget, 0, len(targets))
	for _, target := range targets {
		if strings.ContainsAny(target.Path, "$`") {
			continue
		}
		out = append(out, target)
	}
	return out
}

// commandSearchesWorkingTree reports whether any segment runs a code-search
// tool that scans the working tree when given no path operand.
func commandSearchesWorkingTree(command string) bool {
	for _, segment := range splitChain(command) {
		fields := shellFields(strings.TrimSpace(segment))
		if len(fields) == 0 {
			continue
		}
		if segmentSearchesWorkingTree(fields) {
			return true
		}
	}
	return false
}

func segmentSearchesWorkingTree(fields []string) bool {
	argv0 := filepath.Base(fields[0])
	if slices.Contains(recurseByDefaultTools, argv0) {
		return true
	}
	if slices.Contains(grepTools, argv0) {
		return hasRecursiveFlag(fields[1:])
	}
	// git grep is intentionally not treated as a working-tree code search: it is
	// an exact-text search over tracked files (conflict markers, literal
	// strings) that semantic search cannot replace, so it must not be gated.
	return false
}

// hasRecursiveFlag reports whether the grep arguments request recursion, either
// as a long flag or inside a short-flag bundle such as -rn or -Rl.
func hasRecursiveFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--recursive" {
			return true
		}
		if len(arg) > 1 && arg[0] == '-' && arg[1] != '-' && strings.ContainsAny(arg, "rR") {
			return true
		}
	}
	return false
}
