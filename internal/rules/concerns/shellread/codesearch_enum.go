package shellread

import (
	"path/filepath"
	"slices"
	"strings"
)

// enumerators list files for downstream processing. find and fd walk a
// directory tree; git ls-files lists tracked paths from the working tree. When
// their output is fed to a searcher that reads file contents (find DIR | xargs
// grep, find DIR -exec grep, git ls-files | xargs rg), the effective code-search
// target is the enumerated directory rather than any operand the searcher names,
// so ExtractReadTargets cannot see it. A bare enumeration (find DIR -name
// '*.go') or one whose output is only name-filtered by a stdin grep (find DIR |
// grep X) is handled separately by recursiveEnumerationTargets, which treats a
// recursive structure discovery as a read of the enumerated directory.
var (
	findTools = []string{"find"}
	fdTools   = []string{"fd", "fdfind"}
)

// findExecFlags introduce a command that find runs per match.
var findExecFlags = []string{"-exec", "-execdir"}

// enumeratorCodeSearchTargets returns the directories an enumerator-driven code
// search reads when no searcher operand was resolvable, scoped to tools, the
// rule-declared content-searcher argv0 set. It covers the shapes the operand
// parser misses because the searcher's paths come from the enumerator rather
// than its own operands: an enumerator whose output is run over file contents
// through xargs (find DIR ... | xargs grep, git ls-files | xargs rg) and
// find DIR ... -exec grep. The enumerated directory is the target; the
// index-aware validator decides whether it is in scope. A bare enumeration or a
// pipe into a stdin-reading searcher is not handled here; recursiveEnumerationTargets
// covers those recursive structure-discovery shapes.
func enumeratorCodeSearchTargets(command, cwd string, tools map[string]bool) []ReadTarget {
	var out []ReadTarget
	for _, stages := range commandPipelines(command) {
		searcherIndex := pipelineXargsSearcherIndex(stages, tools)
		for stageIndex, stage := range stages {
			fields := shellFields(strings.TrimSpace(stage))
			if len(fields) == 0 {
				continue
			}
			dirs, ok := enumeratorDirs(fields, cwd)
			if !ok {
				continue
			}
			readsFileContents := searcherIndex > stageIndex || findRunsSearcher(fields, tools)
			if !readsFileContents {
				continue
			}
			for _, dir := range dirs {
				out = append(out, ReadTarget{
					Path:   dir,
					Remote: false,
					Spec:   "code-search-enum",
					Raw:    command,
				})
			}
		}
	}
	return out
}

// pipelineXargsSearcherIndex returns the stage index of the first stage that
// runs a declared content searcher over the enumerated files via xargs, or -1.
// A bare searcher stage (find ... | grep X) is excluded: it reads the filename
// list on stdin, so it is a filename filter, not a search over file contents.
func pipelineXargsSearcherIndex(stages []string, tools map[string]bool) int {
	for i, stage := range stages {
		if stageRunsSearcherOverFiles(shellFields(strings.TrimSpace(stage)), tools) {
			return i
		}
	}
	return -1
}

// stageRunsSearcherOverFiles reports whether a pipeline stage hands the
// enumerated paths to a declared content searcher as arguments, i.e. xargs
// invoking a searcher. That is the form that greps file contents rather than
// filenames.
func stageRunsSearcherOverFiles(fields []string, tools map[string]bool) bool {
	if len(fields) == 0 || filepath.Base(fields[0]) != "xargs" {
		return false
	}
	for _, field := range fields[1:] {
		if tools[filepath.Base(field)] {
			return true
		}
	}
	return false
}

// enumeratorDirs returns the resolved directories an enumerator stage reads and
// whether the stage is an enumerator at all. find reports its leading path
// operands (default cwd); fd and git ls-files default to the working tree.
func enumeratorDirs(fields []string, cwd string) ([]string, bool) {
	argv0 := filepath.Base(fields[0])
	switch {
	case slices.Contains(findTools, argv0):
		return resolvedDirs(findPaths(fields), cwd), true
	case slices.Contains(fdTools, argv0):
		if cwd == "" {
			return nil, true
		}
		return []string{cwd}, true
	case argv0 == "git" && len(fields) >= 2 && fields[1] == "ls-files":
		if cwd == "" {
			return nil, true
		}
		return []string{cwd}, true
	default:
		return nil, false
	}
}

// resolvedDirs resolves each path against cwd.
func resolvedDirs(paths []string, cwd string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, resolvePath(cwd, path))
	}
	return out
}

// findPaths returns find's leading path operands, which precede the first
// expression token (a flag, or a grouping/negation operator). find defaults to
// the current directory when no path is given.
func findPaths(fields []string) []string {
	var paths []string
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") || field == "(" || field == ")" || field == "!" {
			break
		}
		paths = append(paths, field)
	}
	if len(paths) == 0 {
		return []string{"."}
	}
	return paths
}

// findRunsSearcher reports whether a find stage runs a declared content
// searcher through -exec or -execdir, which greps the matched files' contents.
func findRunsSearcher(fields []string, tools map[string]bool) bool {
	if !slices.Contains(findTools, filepath.Base(fields[0])) {
		return false
	}
	for i, field := range fields {
		if !slices.Contains(findExecFlags, field) {
			continue
		}
		if i+1 < len(fields) && tools[filepath.Base(fields[i+1])] {
			return true
		}
	}
	return false
}

// recursiveEnumerationTargets returns the directories a recursive structure
// discovery reads when no content searcher is involved: ls -R, a find with no
// shallow -maxdepth, git ls-files, tree, and a recursive ** glob. A shallow
// ls DIR or find DIR -maxdepth 1 enumerates a single level and stays out of
// scope. The enumerated directory is the target; the index-aware validator
// decides whether it is in scope.
func recursiveEnumerationTargets(command, cwd string) []ReadTarget {
	var out []ReadTarget
	for _, stages := range commandPipelines(command) {
		for _, stage := range stages {
			fields := shellFields(strings.TrimSpace(stage))
			if len(fields) == 0 {
				continue
			}
			for _, dir := range recursiveEnumStageDirs(fields, cwd) {
				out = append(out, enumTarget(dir, command))
			}
		}
	}
	for _, dir := range recursiveGlobDirs(command, cwd) {
		out = append(out, enumTarget(dir, command))
	}
	return out
}

// enumTarget builds a recursive-enumeration read target for a resolved directory.
func enumTarget(dir, command string) ReadTarget {
	return ReadTarget{Path: dir, Remote: false, Spec: "code-search-enum", Raw: command}
}

// recursiveEnumStageDirs returns the directories a single pipeline stage walks
// recursively, or nil when the stage is not a recursive enumerator.
func recursiveEnumStageDirs(fields []string, cwd string) []string {
	argv0 := filepath.Base(fields[0])
	if argv0 == "ls" {
		if !lsIsRecursive(fields) {
			return nil
		}
		return lsOperandDirs(fields, cwd)
	}
	if argv0 == "tree" {
		return lsOperandDirs(fields, cwd)
	}
	if slices.Contains(findTools, argv0) {
		if findIsShallow(fields) {
			return nil
		}
		return resolvedDirs(findPaths(fields), cwd)
	}
	if argv0 == "git" && len(fields) >= 2 && fields[1] == "ls-files" {
		if cwd == "" {
			return nil
		}
		return []string{cwd}
	}
	return nil
}

// lsIsRecursive reports whether an ls invocation requests a recursive listing via
// -R or --recursive, including a combined short flag like -laR.
func lsIsRecursive(fields []string) bool {
	for _, field := range fields[1:] {
		if field == "--recursive" {
			return true
		}
		if len(field) >= 2 && field[0] == '-' && field[1] != '-' && strings.ContainsRune(field[1:], 'R') {
			return true
		}
	}
	return false
}

// lsOperandDirs returns the resolved directory operands of an ls or tree command,
// or cwd when none are given.
func lsOperandDirs(fields []string, cwd string) []string {
	var operands []string
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") {
			continue
		}
		operands = append(operands, field)
	}
	if len(operands) == 0 {
		if cwd == "" {
			return nil
		}
		return []string{cwd}
	}
	return resolvedDirs(operands, cwd)
}

// findIsShallow reports whether a find invocation is limited to one level by
// -maxdepth 0 or -maxdepth 1, which makes it a shallow enumeration.
func findIsShallow(fields []string) bool {
	for index, field := range fields {
		if field != "-maxdepth" {
			continue
		}
		if index+1 < len(fields) && (fields[index+1] == "0" || fields[index+1] == "1") {
			return true
		}
	}
	return false
}

// recursiveGlobDirs returns the base directory of each recursive ** glob token in
// the command: the literal prefix before the first wildcard, taken up to its last
// path separator and resolved against cwd. A command reading files matched by a
// ** glob is searching that subtree's contents.
func recursiveGlobDirs(command, cwd string) []string {
	var out []string
	for _, field := range shellFields(command) {
		if !strings.Contains(field, "**") {
			continue
		}
		prefix := field
		if wildcard := strings.IndexAny(field, "*?["); wildcard >= 0 {
			prefix = field[:wildcard]
		}
		dir := "."
		if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
			dir = prefix[:slash]
			if dir == "" {
				dir = "/"
			}
		}
		out = append(out, resolvePath(cwd, dir))
	}
	return out
}

// commandPipelines splits a command into pipelines (on ;, newline, &&, ||) and
// each pipeline into stages (on a single |), quote and escape aware. It keeps
// the | boundary that splitChain collapses, so a search downstream of an
// enumerator can be told apart from an unrelated command after a ; or &&.
func commandPipelines(command string) [][]string {
	if command == "" {
		return nil
	}
	scanner := &pipelineScanner{
		pipelines: nil,
		stages:    nil,
		current:   strings.Builder{},
		quote:     0,
		escaped:   false,
	}
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		if scanner.consumeQuoted(runes[i]) {
			continue
		}
		if advance := scanner.consumeOperator(runes, i); advance > 0 {
			i += advance - 1
			continue
		}
		scanner.current.WriteRune(runes[i])
	}
	scanner.flushPipeline()
	return scanner.pipelines
}

// pipelineScanner holds the quote, escape, and accumulation state for
// commandPipelines so the per-rune logic splits into small methods.
type pipelineScanner struct {
	pipelines [][]string
	stages    []string
	current   strings.Builder
	quote     rune
	escaped   bool
}

// consumeQuoted handles a rune that is inside a quote or escape, returning true
// when it consumed the rune so the caller skips operator handling for it.
func (s *pipelineScanner) consumeQuoted(r rune) bool {
	if s.escaped {
		s.current.WriteRune(r)
		s.escaped = false
		return true
	}
	if r == '\\' && s.quote != '\'' {
		s.current.WriteRune(r)
		s.escaped = true
		return true
	}
	if s.quote != 0 {
		s.current.WriteRune(r)
		if r == s.quote {
			s.quote = 0
		}
		return true
	}
	if r == '\'' || r == '"' {
		s.current.WriteRune(r)
		s.quote = r
		return true
	}
	return false
}

// consumeOperator flushes at a pipeline boundary (; newline && ||) or a stage
// boundary (a single |), returning the number of runes consumed, or 0 when the
// rune at i is not an operator.
func (s *pipelineScanner) consumeOperator(runes []rune, i int) int {
	r := runes[i]
	if i+1 < len(runes) {
		next := runes[i+1]
		if (r == '&' && next == '&') || (r == '|' && next == '|') {
			s.flushPipeline()
			return 2
		}
	}
	if r == ';' || r == '\n' {
		s.flushPipeline()
		return 1
	}
	if r == '|' {
		s.flushStage()
		return 1
	}
	return 0
}

func (s *pipelineScanner) flushStage() {
	if s.current.Len() > 0 {
		s.stages = append(s.stages, s.current.String())
		s.current.Reset()
	}
}

func (s *pipelineScanner) flushPipeline() {
	s.flushStage()
	if len(s.stages) > 0 {
		s.pipelines = append(s.pipelines, s.stages)
		s.stages = nil
	}
}
