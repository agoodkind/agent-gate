package shellread

import (
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellargs"
	"goodkind.io/gksyntax/shelldecomp"
)

// enumerationTargets selects command directories using condition-owned descriptions.
func enumerationTargets(command string, decomposition *shelldecomp.Decomposition, tools map[string]bool, specs []config.EnumerationSpec) []ReadTarget {
	var targets []ReadTarget
	commands := decomposition.Commands()
	for commandIndex, parsed := range commands {
		for _, spec := range specs {
			if !slices.Contains(spec.Argv0, filepath.Base(parsed.Argv0)) {
				continue
			}
			arguments := shellargs.Parse(parsed, spec.Arguments)
			if !arguments.Selected || arguments.Unresolved {
				continue
			}
			if spec.RequireContentConsumer && !enumerationHasConsumer(commands, commandIndex, arguments, tools, spec) {
				continue
			}
			for _, directory := range enumerationDirectories(arguments, spec) {
				targets = append(targets, enumTarget(directory, command))
			}
		}
	}
	return targets
}

func enumerationDirectories(arguments shellargs.Result, spec config.EnumerationSpec) []string {
	if spec.TargetMode == config.EnumerationTargetCWD {
		if arguments.CWD == "" || arguments.CWD == shelldecomp.Unresolvable {
			return nil
		}
		return []string{arguments.CWD}
	}
	var directories []string
	for _, operand := range arguments.Operands {
		if !operand.Resolvable {
			continue
		}
		if directory := shellargs.ResolvePath(operand.Value, arguments.CWD); directory != "" {
			directories = append(directories, directory)
		}
	}
	if len(directories) == 0 && len(arguments.Operands) == 0 && spec.DefaultCWD && arguments.CWD != "" && arguments.CWD != shelldecomp.Unresolvable {
		return []string{arguments.CWD}
	}
	return directories
}

func enumerationHasConsumer(commands []shelldecomp.Command, index int, arguments shellargs.Result, tools map[string]bool, spec config.EnumerationSpec) bool {
	for _, flag := range spec.ConsumerFlags {
		for _, value := range arguments.Options[flag] {
			if value.Resolvable && tools[filepath.Base(value.Value)] {
				return true
			}
		}
	}
	for next := index + 1; next < len(commands); next++ {
		if !commandsSharePipe(commands[next-1], commands[next]) {
			break
		}
		for _, wrapper := range spec.ConsumerWrappers {
			if !slices.Contains(wrapper.Argv0, filepath.Base(commands[next].Argv0)) {
				continue
			}
			consumer := shellargs.Parse(commands[next], wrapper.Arguments)
			if !consumer.Selected || consumer.Unresolved || len(consumer.Operands) == 0 {
				continue
			}
			program := consumer.Operands[0]
			if program.Resolvable && tools[filepath.Base(program.Value)] {
				return true
			}
		}
	}
	return false
}

func commandsSharePipe(left, right shelldecomp.Command) bool {
	return left.ScopeID == right.ScopeID && left.PipelineEndByte != 0 &&
		left.PipelineStartByte == right.PipelineStartByte && left.PipelineEndByte == right.PipelineEndByte
}

func enumTarget(directory, command string) ReadTarget {
	return ReadTarget{Path: directory, Remote: false, Spec: "code-search-enum", Raw: command}
}

// recursiveGlobDirs returns literal directory prefixes of shell-style recursive globs.
func recursiveGlobDirs(command, cwd string) []string {
	var directories []string
	for _, field := range shellFields(command) {
		if !strings.Contains(field, "**") || !strings.Contains(field, "/") {
			continue
		}
		prefix := field
		if wildcard := strings.IndexAny(field, "*?["); wildcard >= 0 {
			prefix = field[:wildcard]
		}
		directory := "."
		if slash := strings.LastIndex(prefix, "/"); slash >= 0 {
			directory = prefix[:slash]
			if directory == "" {
				directory = "/"
			}
		}
		if strings.ContainsAny(directory, "()") {
			continue
		}
		directories = append(directories, resolvePath(cwd, directory))
	}
	return directories
}
