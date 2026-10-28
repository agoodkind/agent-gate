package shellread

import (
	"os"
	"path/filepath"
	"slices"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/rules/concerns/shellargs"
	"goodkind.io/gksyntax/shelldecomp"
)

func argumentReadTargets(command, cwd string, spec config.ShellReadSpec) []ReadTarget {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	decomposition := shelldecomp.Parse(command, cwd, home)
	var targets []ReadTarget
	for _, parsed := range decomposition.Commands() {
		if !slices.Contains(spec.Argv0, filepath.Base(parsed.Argv0)) {
			continue
		}
		arguments := shellargs.Parse(parsed, *spec.Arguments)
		if arguments.Matched {
			for _, target := range decomposition.ReadTargets() {
				if target.Origin == shelldecomp.ReadOriginRedirect && parsed.Node != nil && target.CommandStartByte == parsed.Node.StartByte && target.Resolvable {
					targets = append(targets, ReadTarget{Path: target.Path, Remote: false, Spec: spec.Name, Raw: command})
				}
			}
		}
		if !arguments.Selected {
			continue
		}
		for _, operand := range arguments.Operands {
			if !operand.Resolvable {
				continue
			}
			value := operandPath(operand.Value, spec)
			if !filepath.IsAbs(value) && (arguments.CWD == "" || arguments.CWD == shelldecomp.Unresolvable) {
				continue
			}
			targets = append(targets, ReadTarget{
				Path:   resolvePath(arguments.CWD, value),
				Remote: false,
				Spec:   spec.Name,
				Raw:    command,
			})
		}
	}
	return targets
}
