package rules

import (
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

const maxBranchMoveArguments = 2

func movedLocalBranches(
	subcommand string,
	args []shelldecomp.Word,
	currentPath string,
	statePath string,
) ([]string, string) {
	switch gitRefSubcommand(subcommand) {
	case gitRefSubcommandBranch:
		return branchMoveTargets(args), statePath
	case gitRefSubcommandUpdateRef:
		return updateRefTargets(args), statePath
	case gitRefSubcommandCheckout:
		return resetBranchTarget(args, "B"), statePath
	case gitRefSubcommandSwitch:
		return resetBranchTarget(args, "C"), statePath
	case gitRefSubcommandPush:
		return localPushTargets(args, currentPath)
	default:
		return nil, ""
	}
}

func branchMoveTargets(args []shelldecomp.Word) []string {
	mode, force, positionals, valid := parseBranchMoveArgs(args)
	if !valid {
		return nil
	}

	if mode == branchMoveNone && force {
		mode = branchMoveForce
	}

	return branchTargetsForMode(mode, positionals)
}

func parseBranchMoveArgs(
	args []shelldecomp.Word,
) (branchMoveMode, bool, []string, bool) {
	mode := branchMoveNone
	force := false
	positionals := make([]string, 0, len(args))
	endOptions := false

	for _, argument := range args {
		if !argument.Resolvable {
			return branchMoveNone, false, nil, false
		}

		value := argument.Value
		if !endOptions && value == "--" {
			endOptions = true

			continue
		}

		if !endOptions && strings.HasPrefix(value, "--") {
			valid := applyBranchLongMove(value, &mode, &force)
			if !valid {
				return branchMoveNone, false, nil, false
			}

			continue
		}

		if !endOptions && strings.HasPrefix(value, "-") && value != "-" {
			shortMode, shortForce, valid := branchShortMove(value)
			if !valid || !setBranchMoveMode(&mode, shortMode) {
				return branchMoveNone, false, nil, false
			}

			force = force || shortForce

			continue
		}

		positionals = append(positionals, value)
	}

	return mode, force, positionals, true
}

func applyBranchLongMove(value string, mode *branchMoveMode, force *bool) bool {
	if value == "--force" {
		*force = true

		return true
	}

	if value == "--delete" {
		return setBranchMoveMode(mode, branchMoveDelete)
	}

	if value == "--move" {
		return setBranchMoveMode(mode, branchMoveRename)
	}

	if value == "--copy" {
		return setBranchMoveMode(mode, branchMoveCopy)
	}

	return false
}

func branchTargetsForMode(
	mode branchMoveMode,
	positionals []string,
) []string {
	switch mode {
	case branchMoveForce:
		if len(positionals) < 1 || len(positionals) > maxBranchMoveArguments {
			return nil
		}

		return localBranchTarget(positionals[0])
	case branchMoveDelete:
		if len(positionals) == 0 {
			return nil
		}

		targets := make([]string, 0, len(positionals))
		for _, positional := range positionals {
			targets = append(targets, localBranchTarget(positional)...)
		}

		return targets
	case branchMoveRename:
		if len(positionals) == 1 {
			return []string{gitCurrentBranchTarget}
		}

		if len(positionals) != maxBranchMoveArguments {
			return nil
		}

		return localBranchTarget(positionals[0])
	case branchMoveCopy:
		return nil
	case branchMoveNone:
		return nil
	}

	return nil
}

func setBranchMoveMode(current *branchMoveMode, next branchMoveMode) bool {
	if next == branchMoveNone {
		return true
	}

	if *current != branchMoveNone && *current != next {
		return false
	}

	*current = next

	return true
}

func branchShortMove(value string) (branchMoveMode, bool, bool) {
	if value == "-f" || value == "-F" {
		return branchMoveNone, true, true
	}

	if value == "-d" {
		return branchMoveDelete, false, true
	}

	if value == "-D" || value == "-df" || value == "-fd" {
		return branchMoveDelete, true, true
	}

	if value == "-m" {
		return branchMoveRename, false, true
	}

	if value == "-M" {
		return branchMoveRename, true, true
	}

	if value == "-fm" || value == "-mf" || value == "-fM" || value == "-Mf" {
		return branchMoveRename, true, true
	}

	if value == "-c" {
		return branchMoveCopy, false, true
	}

	if value == "-C" {
		return branchMoveCopy, true, true
	}

	if value == "-fc" || value == "-cf" || value == "-fC" || value == "-Cf" {
		return branchMoveCopy, true, true
	}

	return branchMoveNone, false, false
}
