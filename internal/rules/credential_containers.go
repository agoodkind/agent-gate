package rules

import (
	"slices"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

func (collector *credentialReadCollector) containerCommand(command shelldecomp.Command, cwd string, depth int) {
	if len(command.Args) < 3 || command.Args[0].Value != "exec" {
		return
	}
	valueFlags := []string{"-u", "--user", "-w", "--workdir", "-e", "--env", "--env-file", "--detach-keys", "-n", "--namespace", "-c", "--container"}
	index := 1
	for index < len(command.Args) && strings.HasPrefix(command.Args[index].Value, "-") {
		if slices.Contains(valueFlags, command.Args[index].Value) {
			index++
		}
		index++
	}
	index++ // The first positional argument selects the container or pod.
	if index < len(command.Args) && command.Args[index].Value == "--" {
		index++
	}
	if index >= len(command.Args) {
		return
	}
	var parts []string
	for _, arg := range command.Args[index:] {
		parts = append(parts, arg.Text)
	}
	collector.shell(strings.Join(parts, " "), cwd, true, depth-1)
}
