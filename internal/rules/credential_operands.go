package rules

import (
	"slices"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

func (collector *credentialReadCollector) readerOperands(command shelldecomp.Command, cwd string, remote bool) {
	if command.Argv0 == "dd" {
		for _, arg := range command.Args {
			if path, ok := strings.CutPrefix(arg.Value, "if="); ok {
				collector.add(path, cwd, !remote)
			}
		}
		return
	}
	if command.Argv0 == "git" {
		collector.gitSources(command, cwd, remote)
		return
	}
	positions := credentialPositionals(command.Args)
	if slices.Contains([]string{"cp", "mv", "install", "scp", "rsync"}, command.Argv0) {
		if slices.ContainsFunc(command.Args, func(arg shelldecomp.Word) bool {
			return arg.Value == "-t" || arg.Value == "--target-directory" || strings.HasPrefix(arg.Value, "--target-directory=")
		}) {
			collector.operands(positions, cwd, remote)
			return
		}
		if len(positions) > 1 {
			collector.operands(positions[:len(positions)-1], cwd, remote)
		}
		return
	}
	if command.Argv0 == "tar" {
		collector.operands(positions, cwd, remote)
		return
	}
	if command.Argv0 == "zip" {
		if len(positions) > 1 {
			collector.operands(positions[1:], cwd, remote)
		}
		return
	}
	if slices.Contains([]string{"base64", "strings", "xxd", "hexdump", "od"}, command.Argv0) {
		for _, arg := range command.Args {
			if !strings.HasPrefix(arg.Value, "-") {
				collector.add(arg.Value, cwd, !remote)
			}
		}
	}
}

func (collector *credentialReadCollector) operands(positions []string, cwd string, remote bool) {
	for _, path := range positions {
		collector.add(path, cwd, !remote)
	}
}

func credentialPositionals(args []shelldecomp.Word) []string {
	valueFlags := []string{"-f", "--file", "-C", "--directory", "-t", "--target-directory", "-S", "--suffix", "-m", "--mode", "-o", "--owner", "-g", "--group", "-e", "--rsh"}
	var positions []string
	for index := 0; index < len(args); index++ {
		value := args[index].Value
		if strings.HasPrefix(value, "-") {
			if slices.Contains(valueFlags, value) || (strings.HasPrefix(value, "-") && !strings.HasPrefix(value, "--") && strings.HasSuffix(value, "f")) {
				index++
			}
			continue
		}
		positions = append(positions, value)
	}
	return positions
}

func (collector *credentialReadCollector) gitSources(command shelldecomp.Command, cwd string, remote bool) {
	cwd, words, ok := applyCwdFlagWords(cwd, command.Args, []string{"-C"})
	if !ok {
		return
	}
	subcommand, index := commandSubcommand("git", words)
	if subcommand == "diff" {
		collector.operands(credentialPositionals(words[index+1:]), cwd, remote)
	}
	if subcommand == "show" {
		for _, word := range words[index+1:] {
			if _, path, found := strings.Cut(word.Value, ":"); found {
				collector.add(path, cwd, !remote)
			}
		}
	}
}
