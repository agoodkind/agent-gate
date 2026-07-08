package oracle

import (
	"path"
	"regexp"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

// Verdict is a deterministic oracle decision.
type Verdict int

const (
	// Block means the command matches a protected operation.
	Block Verdict = iota
	// Allow means the command is provably outside the protected operation.
	Allow
	// Unknown means the oracle could not resolve the command precisely.
	Unknown
)

// String returns the stable lowercase label for verdict.
func (verdict Verdict) String() string {
	switch verdict {
	case Block:
		return "block"
	case Allow:
		return "allow"
	case Unknown:
		return "unknown"
	default:
		return "invalid"
	}
}

// Search classifies whether command searches indexed file contents.
func Search(command, cwd string, roots []string) Verdict {
	normalizedRoots := normalizeRoots(roots)
	if containsDynamicCommand(command) {
		return Unknown
	}
	if verdict, handled := classifyXargs(command, cwd, normalizedRoots); handled {
		return verdict
	}

	decomposition := shelldecomp.Parse(command, cwd, homeDir)
	if decomposition.IsOpaque() {
		return Unknown
	}
	if containsOpaqueExecutor(decomposition) {
		return Unknown
	}
	if verdict, handled := classifyFindExec(decomposition, normalizedRoots); handled {
		if verdict == Block || verdict == Unknown {
			return verdict
		}
	}

	sawContentSearch := false
	for _, target := range decomposition.ReadTargets() {
		if !isContentSearchRead(target.Argv0) {
			continue
		}
		sawContentSearch = true
		targetPath := target.Path
		if !target.Resolvable {
			resolved, ok := resolveRawTarget(target, decomposition.Assignments())
			if !ok {
				return Unknown
			}
			targetPath = resolved
		}
		if isIndexed(targetPath, normalizedRoots) {
			return Block
		}
	}
	if sawContentSearch || containsSearchCommand(decomposition) {
		return Allow
	}
	return Unknown
}

var variableRefPattern = regexp.MustCompile(`\$\{?([A-Za-z_][A-Za-z0-9_]*)\}?`)

func resolveRawTarget(
	target shelldecomp.ReadTarget,
	assignments []shelldecomp.Assignment,
) (string, bool) {
	raw := stripOuterQuotes(target.Raw)
	if containsDynamicCommand(raw) {
		return "", false
	}
	values := map[string]string{"HOME": homeDir}
	for _, assignment := range assignments {
		if assignment.ScopeID != target.ScopeID || !assignment.Resolvable || assignment.Name == "" {
			continue
		}
		values[assignment.Name] = assignment.Value
	}

	unknown := false
	expanded := variableRefPattern.ReplaceAllStringFunc(raw, func(match string) string {
		name := variableName(match)
		value, found := values[name]
		if !found {
			unknown = true
			return match
		}
		return value
	})
	if unknown || strings.Contains(expanded, "$") {
		return "", false
	}
	resolved := resolvePath(expanded, target.Cwd)
	return resolved, resolved != ""
}

func stripOuterQuotes(value string) string {
	if len(value) < 2 {
		return value
	}
	first := value[0]
	last := value[len(value)-1]
	if first == last && (first == '\'' || first == '"') {
		return value[1 : len(value)-1]
	}
	return value
}

func variableName(match string) string {
	trimmed := strings.TrimPrefix(match, "$")
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed[1 : len(trimmed)-1]
	}
	return trimmed
}

const homeDir = "/Users/agoodkind"

var contentSearchReaders = map[string]struct{}{
	"grep":     {},
	"egrep":    {},
	"fgrep":    {},
	"rgrep":    {},
	"rg":       {},
	"ripgrep":  {},
	"ag":       {},
	"ack":      {},
	"git grep": {},
	"sed":      {},
	"awk":      {},
	"gawk":     {},
	"nawk":     {},
	"perl":     {},
	"nl":       {},
}

var searchCommands = map[string]struct{}{
	"grep":    {},
	"egrep":   {},
	"fgrep":   {},
	"rgrep":   {},
	"rg":      {},
	"ripgrep": {},
	"ag":      {},
	"ack":     {},
}

func normalizeRoots(roots []string) []string {
	normalized := make([]string, 0, len(roots))
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		normalized = append(normalized, path.Clean(root))
	}
	return normalized
}

func containsDynamicCommand(command string) bool {
	return strings.Contains(command, "$(") || strings.Contains(command, "`")
}

func containsOpaqueExecutor(decomposition *shelldecomp.Decomposition) bool {
	for _, command := range decomposition.Commands() {
		if command.Argv0 == "eval" {
			return true
		}
		if !isShell(command.Argv0) {
			continue
		}
		for _, argument := range command.Args {
			if argument.Text == "-c" || argument.Value == "-c" {
				return true
			}
		}
	}
	return false
}

func isShell(argv0 string) bool {
	return argv0 == "sh" || argv0 == "bash" || argv0 == "zsh"
}

func classifyXargs(command, cwd string, roots []string) (Verdict, bool) {
	stages := splitPipelineStages(command)
	for index, stage := range stages {
		decomposition := shelldecomp.Parse(stage, cwd, homeDir)
		if decomposition.IsOpaque() {
			continue
		}
		xargsCommand, found := firstCommand(decomposition, "xargs")
		if !found || !xargsRunsSearcher(xargsCommand) {
			continue
		}
		if index == 0 {
			return Unknown, true
		}
		return classifyXargsProducer(stages[index-1], cwd, roots), true
	}
	return Unknown, false
}

func splitPipelineStages(command string) []string {
	parts := strings.Split(command, "|")
	stages := make([]string, 0, len(parts))
	for _, part := range parts {
		stage := strings.TrimSpace(part)
		if stage != "" {
			stages = append(stages, stage)
		}
	}
	return stages
}

func firstCommand(decomposition *shelldecomp.Decomposition, argv0 string) (shelldecomp.Command, bool) {
	for _, command := range decomposition.Commands() {
		if command.Argv0 == argv0 {
			return command, true
		}
	}
	return shelldecomp.Command{}, false
}

func xargsRunsSearcher(command shelldecomp.Command) bool {
	for _, argument := range command.Args {
		if strings.HasPrefix(argument.Text, "-") {
			continue
		}
		if _, found := searchCommands[path.Base(argument.Value)]; found {
			return true
		}
		return false
	}
	return false
}

func classifyXargsProducer(stage, cwd string, roots []string) Verdict {
	decomposition := shelldecomp.Parse(stage, cwd, homeDir)
	if decomposition.IsOpaque() {
		return Unknown
	}
	if command, found := firstCommand(decomposition, "echo"); found {
		return classifyEchoProducer(command, roots)
	}
	if command, found := firstCommand(decomposition, "find"); found {
		return classifyFindRoot(command, roots)
	}
	return Unknown
}

func classifyEchoProducer(command shelldecomp.Command, roots []string) Verdict {
	sawUnknown := false
	for _, argument := range command.Args {
		if !argument.Resolvable {
			sawUnknown = true
			continue
		}
		resolved := resolvePath(argument.Value, command.Cwd)
		if resolved == "" {
			sawUnknown = true
			continue
		}
		if isIndexed(resolved, roots) {
			return Block
		}
	}
	if sawUnknown {
		return Unknown
	}
	return Allow
}

func classifyFindExec(
	decomposition *shelldecomp.Decomposition,
	roots []string,
) (Verdict, bool) {
	for _, command := range decomposition.Commands() {
		if command.Argv0 != "find" || !findExecRunsSearcher(command) {
			continue
		}
		return classifyFindRoot(command, roots), true
	}
	return Unknown, false
}

func findExecRunsSearcher(command shelldecomp.Command) bool {
	for index, argument := range command.Args {
		if argument.Text != "-exec" && argument.Value != "-exec" {
			continue
		}
		if index+1 >= len(command.Args) {
			return false
		}
		next := command.Args[index+1]
		if next.Text == ";" || next.Text == "\\;" || next.Text == "+" {
			return false
		}
		if !next.Resolvable {
			return true
		}
		_, found := searchCommands[path.Base(next.Value)]
		return found
	}
	return false
}

func classifyFindRoot(command shelldecomp.Command, roots []string) Verdict {
	for _, argument := range command.Args {
		if argument.Text == "-exec" || argument.Value == "-exec" {
			break
		}
		if strings.HasPrefix(argument.Text, "-") {
			continue
		}
		if !argument.Resolvable {
			return Unknown
		}
		resolved := resolvePath(argument.Value, command.Cwd)
		if resolved == "" {
			return Unknown
		}
		if isIndexed(resolved, roots) {
			return Block
		}
		return Allow
	}
	return Unknown
}

func resolvePath(value, cwd string) string {
	if value == "" || cwd == shelldecomp.Unresolvable {
		return ""
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(value)
	}
	if cwd == "" {
		return ""
	}
	return path.Clean(path.Join(cwd, value))
}

func isContentSearchRead(argv0 string) bool {
	_, found := contentSearchReaders[argv0]
	return found
}

func containsSearchCommand(decomposition *shelldecomp.Decomposition) bool {
	for _, command := range decomposition.Commands() {
		if _, found := searchCommands[command.Argv0]; found {
			return true
		}
		if command.Argv0 != "git" {
			continue
		}
		if len(command.Args) > 0 && command.Args[0].Resolvable && command.Args[0].Value == "grep" {
			return true
		}
	}
	return false
}

func isIndexed(candidate string, roots []string) bool {
	if candidate == "" || candidate == shelldecomp.Unresolvable {
		return false
	}
	cleanCandidate := path.Clean(candidate)
	for _, root := range roots {
		if cleanCandidate == root || strings.HasPrefix(cleanCandidate, root+"/") {
			return true
		}
	}
	return false
}
