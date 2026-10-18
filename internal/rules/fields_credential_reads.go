package rules

import (
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
	"goodkind.io/gksyntax/shelldecomp"
)

const (
	credentialSourceLimit   = 1 << 20
	credentialSourceDepth   = 8
	uninspectedScriptMarker = "<uninspected-script>"
	unresolvedReadMarker    = "<unresolved-read>"
)

type credentialReadCollector struct {
	paths           []string
	seen            map[string]bool
	scripts         map[string]bool
	remaining       int
	protectedSource func(string) bool
}

// CredentialReadTargets collects read paths and inspects bounded local helper
// source under active policy. Protected helpers and helpers without evaluation
// policy remain uninspected and produce a marker for the configured rule.
func (fields FieldSet) CredentialReadTargets() string {
	collector := credentialReadCollector{
		paths: nil, seen: make(map[string]bool), scripts: make(map[string]bool),
		remaining: credentialSourceLimit, protectedSource: fields.protectedSource,
	}
	for _, path := range []string{fields.ToolInputFilePath, fields.ToolInputPath, fields.FilePath, fields.Path} {
		collector.add(path, fields.BaseCWD(), true)
	}
	for _, command := range []string{fields.ToolInputCommand, fields.Command} {
		if strings.Contains(command, "*** Begin Patch") {
			collector.patch(command, fields.BaseCWD())
			continue
		}
		collector.shell(command, fields.BaseCWD(), false, credentialSourceDepth)
	}
	if credentialScriptPath(fields.FilePathValue()) {
		bodies := []string{fields.ToolInputContent, fields.ToolInputNewString}
		bodies = append(bodies, fields.EditsNewString...)
		bodies = append(bodies, fields.EditsNewLine...)
		for _, body := range bodies {
			collector.source(body, fields.FilePathValue(), fields.BaseCWD(), credentialSourceDepth)
		}
	}
	return strings.Join(collector.paths, "\n")
}

func (collector *credentialReadCollector) add(path, cwd string, local bool) {
	if len(path) >= 2 && ((path[0] == '"' && path[len(path)-1] == '"') || (path[0] == '\'' && path[len(path)-1] == '\'')) {
		path = path[1 : len(path)-1]
	}
	if path == "" || path == shelldecomp.Unresolvable {
		return
	}
	if local && !strings.ContainsAny(path, "$`\n") {
		path = resolvePath(cwd, path)
	}
	if !collector.seen[path] {
		collector.seen[path] = true
		collector.paths = append(collector.paths, path)
	}
	if local {
		collector.addSymlink(path)
	}
}

func (collector *credentialReadCollector) shell(source, cwd string, remote bool, depth int) {
	if strings.TrimSpace(source) == "" {
		return
	}
	if depth <= 0 || len(source) > credentialSourceLimit {
		collector.add(uninspectedScriptMarker, "", false)
		return
	}
	source = shellparse.ExpandLiteralAssignments(source)
	options := shelldecomp.Options{Home: cwd, FileResolver: nil}
	parsed := shelldecomp.ParseWithOptions(source, cwd, options)
	collector.decomposition(parsed, cwd, remote, depth)
	collector.redirects(source, cwd, !remote)
}

func (collector *credentialReadCollector) decomposition(parsed *shelldecomp.Decomposition, cwd string, remote bool, depth int) {
	if depth <= 0 {
		collector.add(uninspectedScriptMarker, "", false)
		return
	}
	for _, target := range parsed.ReadTargets() {
		path := target.Path
		if !target.Resolvable {
			path = target.Raw
			collector.add(unresolvedReadMarker, "", false)
		}
		collector.add(path, cwd, !remote)
	}
	for _, command := range parsed.Commands() {
		collector.command(command, cwd, remote, depth)
	}
	for _, region := range parsed.EmbeddedRegions() {
		if region.Parsed != nil && region.Lang != shelldecomp.LangShell {
			collector.decomposition(region.Parsed, cwd, remote, depth-1)
		}
		if region.Lang == shelldecomp.LangOpaque {
			collector.shell(region.Text, cwd, remote, depth-1)
		}
	}
}

func (collector *credentialReadCollector) source(body, path, cwd string, depth int) {
	if credentialInterpreter(path) == "ruby" {
		collector.rubySource(body, cwd, depth)
		return
	}
	if !credentialShellPath(path) {
		flag := "-e"
		if credentialInterpreter(path) == "python3" {
			flag = "-c"
		}
		if credentialInterpreter(path) == "php" {
			flag = "-r"
		}
		command := credentialInterpreter(path) + " " + flag + " " + shellQuotePath(body)
		collector.shell(command, cwd, false, depth)
		return
	}
	collector.shell(body, cwd, false, depth)
}

func credentialShellPath(path string) bool {
	return slices.Contains([]string{".sh", ".bash", ".zsh", ".ksh"}, strings.ToLower(filepath.Ext(path)))
}

func credentialScriptPath(path string) bool {
	return credentialShellPath(path) || credentialInterpreter(path) != ""
}

func credentialInterpreter(path string) string {
	interpreters := map[string]string{
		".py": "python3", ".js": "node", ".mjs": "node", ".cjs": "node",
		".rb": "ruby", ".pl": "perl", ".php": "php",
	}
	return interpreters[strings.ToLower(filepath.Ext(path))]
}

func shellQuotePath(path string) string {
	return "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "'"
}
