package rules

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
	"goodkind.io/gksyntax/shelldecomp"
)

func (collector *credentialReadCollector) addSymlink(path string) {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil && resolved != path {
		collector.add(resolved, "", false)
	}
}

func (collector *credentialReadCollector) readSource(path string) ([]byte, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !credentialScriptPath(resolved) || protectedCredentialSource(resolved) {
		collector.add(uninspectedScriptMarker, "", false)
		return nil, false
	}
	collector.add(resolved, "", false)
	file, err := os.OpenFile(resolved, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		collector.add(uninspectedScriptMarker, "", false)
		return nil, false
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(collector.remaining) {
		collector.add(uninspectedScriptMarker, "", false)
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(file, int64(collector.remaining)+1))
	if err != nil || len(data) > collector.remaining {
		collector.add(uninspectedScriptMarker, "", false)
		return nil, false
	}
	collector.remaining -= len(data)
	return data, true
}

func (collector *credentialReadCollector) command(command shelldecomp.Command, cwd string, remote bool, depth int) {
	if command.Cwd != "" && command.Cwd != shelldecomp.Unresolvable {
		cwd = command.Cwd
	}
	if command.Argv0 == "ssh" {
		collector.remoteCommand(command, cwd, depth)
		return
	}
	if slices.Contains([]string{"docker", "podman", "kubectl"}, command.Argv0) {
		collector.containerCommand(command, cwd, depth)
	}
	collector.readerOperands(command, cwd, remote)
	if command.Kind == shelldecomp.CommandKindInterpreter && !localCredentialShell(command.Argv0) {
		collector.interpreter(command, cwd, remote, depth)
	}
	if localCredentialShell(command.Argv0) {
		collector.shellInvocation(command.Args, cwd, remote, depth)
	}
	if credentialScriptPath(command.Argv0) {
		collector.helper(credentialCommandPath(command), cwd, remote, depth)
	}
}

func (collector *credentialReadCollector) interpreter(command shelldecomp.Command, cwd string, remote bool, depth int) {
	for _, arg := range command.Args {
		if credentialScriptPath(arg.Value) && arg.Kind != shelldecomp.WordKindEmbeddedCode {
			collector.helper(arg.Value, cwd, remote, depth)
		}
	}
}

func (collector *credentialReadCollector) shellInvocation(args []shelldecomp.Word, cwd string, remote bool, depth int) {
	for _, arg := range args {
		if credentialInlineShellFlag(arg.Value) {
			collector.inlineShell(args, cwd, remote, depth)
			return
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg.Value, "-") {
			continue
		}
		collector.helper(arg.Value, cwd, remote, depth)
		return
	}
}

func credentialCommandPath(command shelldecomp.Command) string {
	if command.Node != nil {
		for _, word := range shellFields(command.Node.Text) {
			if filepath.Base(word) == command.Argv0 {
				return word
			}
		}
	}
	return command.Argv0
}

func localCredentialShell(argv0 string) bool {
	return slices.Contains([]string{"bash", "sh", "zsh", "dash", "ksh", "source", "."}, argv0)
}

func (collector *credentialReadCollector) inlineShell(args []shelldecomp.Word, cwd string, remote bool, depth int) {
	for index, arg := range args {
		if credentialInlineShellFlag(arg.Value) && index+1 < len(args) {
			collector.shell(args[index+1].Value, cwd, remote, depth-1)
			return
		}
	}
}

func (collector *credentialReadCollector) helper(path, cwd string, remote bool, depth int) {
	if path == "-" {
		return
	}
	if remote || strings.ContainsAny(path, "$`") || depth <= 0 {
		collector.add(uninspectedScriptMarker, "", false)
		return
	}
	path = resolvePath(cwd, path)
	if collector.scripts[path] {
		return
	}
	collector.scripts[path] = true
	data, ok := collector.readSource(path)
	if ok {
		collector.source(string(data), path, cwd, depth-1)
	}
}

func credentialInlineShellFlag(value string) bool {
	return strings.HasPrefix(value, "-") && strings.HasSuffix(value, "c")
}

// protectedCredentialSource prevents helper analysis from reading credential
// contents. The collector reports the path for policy evaluation instead.
func protectedCredentialSource(path string) bool {
	for component := range strings.SplitSeq(filepath.ToSlash(strings.ToLower(path)), "/") {
		if component == ".env" || strings.HasPrefix(component, ".env.") || component == "credentials" || component == "secrets" {
			return true
		}
	}
	return false
}

func (collector *credentialReadCollector) remoteCommand(command shelldecomp.Command, cwd string, depth int) {
	valueFlags := map[string]bool{"-p": true, "-i": true, "-F": true, "-J": true, "-l": true, "-o": true, "-S": true, "-W": true}
	index := 0
	for index < len(command.Args) && strings.HasPrefix(command.Args[index].Value, "-") {
		if valueFlags[command.Args[index].Value] {
			index++
		}
		index++
	}
	index++ // The first positional argument selects the SSH destination.
	if index >= len(command.Args) {
		return
	}
	var parts []string
	for _, arg := range command.Args[index:] {
		parts = append(parts, arg.Value)
	}
	collector.shell(strings.Join(parts, " "), cwd, true, depth-1)
}
