package oracle

import (
	"path/filepath"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

// Worktree classifies whether command violates primary/default worktree rules.
func Worktree(command, cwd string, st State) Verdict {
	if containsDynamicCommand(command) {
		return Unknown
	}
	state := normalizeState(st)
	decomposition := shelldecomp.Parse(command, cwd, homeDir)
	if decomposition.IsOpaque() || containsEvalCommand(decomposition) {
		return Unknown
	}

	sawUnknown := false
	if verdict := classifyWriteTargets(decomposition, state); verdict == Block {
		return Block
	} else if verdict == Unknown {
		sawUnknown = true
	}
	for _, command := range decomposition.Commands() {
		if verdict := classifyWriteCommand(command, state); verdict == Block {
			return Block
		} else if verdict == Unknown {
			sawUnknown = true
		}
		if worktreeShellCommand(command.Argv0) != worktreeShellCommandGit {
			continue
		}
		if verdict := classifyGit(command, state); verdict == Block {
			return Block
		} else if verdict == Unknown {
			sawUnknown = true
		}
	}
	if sawUnknown {
		return Unknown
	}
	return Allow
}

func normalizeState(state State) State {
	out := State{
		PrimaryCheckout: cleanPath(state.PrimaryCheckout),
		DefaultBranch:   branchName(state.DefaultBranch),
		Worktrees:       make([]WorktreeEntry, 0, len(state.Worktrees)),
		CurrentWorktree: cleanPath(state.CurrentWorktree),
		CurrentBranch:   branchName(state.CurrentBranch),
	}
	for _, worktree := range state.Worktrees {
		out.Worktrees = append(out.Worktrees, WorktreeEntry{
			Path:      cleanPath(worktree.Path),
			Branch:    branchName(worktree.Branch),
			IsPrimary: worktree.IsPrimary,
		})
	}
	return out
}

func containsEvalCommand(decomposition *shelldecomp.Decomposition) bool {
	for _, command := range decomposition.Commands() {
		if worktreeShellCommand(command.Argv0) == worktreeShellCommandEval {
			return true
		}
	}
	return false
}

func classifyWriteTargets(decomposition *shelldecomp.Decomposition, state State) Verdict {
	sawUnknown := false
	for _, target := range decomposition.WriteTargets() {
		if !target.Resolvable {
			sawUnknown = true
			continue
		}
		if writeVerdictForPath(target.Path, state) == Block {
			return Block
		}
	}
	if sawUnknown {
		return Unknown
	}
	return Allow
}

func classifyWriteCommand(command shelldecomp.Command, state State) Verdict {
	switch worktreeShellCommand(command.Argv0) {
	case worktreeShellCommandTee, worktreeShellCommandRM, worktreeShellCommandMkdir, worktreeShellCommandTouch:
		return classifyWordTargets(commandPathOperands(command.Args), command.Cwd, state)
	case worktreeShellCommandCP:
		operands := commandPathOperands(command.Args)
		if len(operands) < 2 {
			return Allow
		}
		return classifyWordTargets(operands[len(operands)-1:], command.Cwd, state)
	case worktreeShellCommandMV, worktreeShellCommandVI, worktreeShellCommandVim, worktreeShellCommandNVim,
		worktreeShellCommandNano, worktreeShellCommandEmacs, worktreeShellCommandED, worktreeShellCommandCode:
		return classifyWordTargets(commandPathOperands(command.Args), command.Cwd, state)
	case worktreeShellCommandSed:
		if !sedInPlace(command.Args) {
			return Allow
		}
		return classifyWordTargets(sedFileOperands(command.Args), command.Cwd, state)
	case worktreeShellCommandEval, worktreeShellCommandGit:
		return Allow
	default:
		return Allow
	}
}

type worktreeShellCommand string

const (
	worktreeShellCommandCode  worktreeShellCommand = "code"
	worktreeShellCommandCP    worktreeShellCommand = "cp"
	worktreeShellCommandED    worktreeShellCommand = "ed"
	worktreeShellCommandEmacs worktreeShellCommand = "emacs"
	worktreeShellCommandEval  worktreeShellCommand = "eval"
	worktreeShellCommandGit   worktreeShellCommand = "git"
	worktreeShellCommandMkdir worktreeShellCommand = "mkdir"
	worktreeShellCommandMV    worktreeShellCommand = "mv"
	worktreeShellCommandNano  worktreeShellCommand = "nano"
	worktreeShellCommandNVim  worktreeShellCommand = "nvim"
	worktreeShellCommandRM    worktreeShellCommand = "rm"
	worktreeShellCommandSed   worktreeShellCommand = "sed"
	worktreeShellCommandTee   worktreeShellCommand = "tee"
	worktreeShellCommandTouch worktreeShellCommand = "touch"
	worktreeShellCommandVI    worktreeShellCommand = "vi"
	worktreeShellCommandVim   worktreeShellCommand = "vim"
)

func commandPathOperands(args []shelldecomp.Word) []shelldecomp.Word {
	operands := make([]shelldecomp.Word, 0, len(args))
	endFlags := false
	skipNext := false
	for _, argument := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if !endFlags && argument.Text == "--" {
			endFlags = true
			continue
		}
		if !endFlags && strings.HasPrefix(argument.Text, "-") && argument.Text != "-" {
			if writeValueFlag(argument.Text) {
				skipNext = true
			}
			continue
		}
		operands = append(operands, argument)
	}
	return operands
}

func writeValueFlag(flag string) bool {
	return flag == "-m" || flag == "-t" || flag == "-T" ||
		flag == "--target-directory" || flag == "--mode" || flag == "--reference"
}

func classifyWordTargets(words []shelldecomp.Word, cwd string, state State) Verdict {
	sawUnknown := false
	for _, word := range words {
		if word.Value == "-" {
			continue
		}
		if !word.Resolvable {
			sawUnknown = true
			continue
		}
		resolved := resolvePath(word.Value, cwd)
		if resolved == "" {
			sawUnknown = true
			continue
		}
		if writeVerdictForPath(resolved, state) == Block {
			return Block
		}
	}
	if sawUnknown {
		return Unknown
	}
	return Allow
}

func sedInPlace(args []shelldecomp.Word) bool {
	for _, argument := range args {
		if argument.Text == "-i" || strings.HasPrefix(argument.Text, "-i.") ||
			argument.Text == "--in-place" || strings.HasPrefix(argument.Text, "--in-place=") {
			return true
		}
	}
	return false
}

func sedFileOperands(args []shelldecomp.Word) []shelldecomp.Word {
	operands := make([]shelldecomp.Word, 0, len(args))
	skipNext := false
	scriptConsumed := false
	for index := range args {
		argument := args[index]
		if skipNext {
			skipNext = false
			continue
		}
		if argument.Text == "-e" || argument.Text == "-f" {
			skipNext = true
			scriptConsumed = true
			continue
		}
		if argument.Text == "-i" || strings.HasPrefix(argument.Text, "-i.") ||
			argument.Text == "--in-place" || strings.HasPrefix(argument.Text, "--in-place=") {
			continue
		}
		if strings.HasPrefix(argument.Text, "-") && argument.Text != "-" {
			continue
		}
		if !scriptConsumed {
			scriptConsumed = true
			continue
		}
		operands = append(operands, argument)
	}
	return operands
}

func writeVerdictForPath(candidate string, state State) Verdict {
	if isUnderPath(candidate, state.PrimaryCheckout) {
		return Block
	}
	worktree, found := worktreeForPath(candidate, state)
	if found && worktree.Branch == state.DefaultBranch {
		return Block
	}
	return Allow
}

func classifyGit(command shelldecomp.Command, state State) Verdict {
	subcommand, args, effectiveCwd, ok := gitSubcommand(command)
	if !ok {
		return Unknown
	}
	if isGitRead(subcommand, args) {
		return Allow
	}
	effectiveWorktree, effectiveBranch := currentContext(effectiveCwd, state)
	mutation, refTargets, unknownRef := gitMutationAndRefs(subcommand, args, effectiveBranch)
	if mutation && (samePath(effectiveWorktree, state.PrimaryCheckout) ||
		effectiveBranch == state.DefaultBranch) {
		return Block
	}
	otherBranches := otherWorktreeBranches(effectiveWorktree, state)
	for _, refTarget := range refTargets {
		branch := branchFromRef(refTarget)
		if _, found := otherBranches[branch]; found {
			return Block
		}
	}
	if unknownRef {
		return Unknown
	}
	return Allow
}

func gitSubcommand(command shelldecomp.Command) (gitSubcommandName, []shelldecomp.Word, string, bool) {
	effectiveCwd := command.Cwd
	for index := 0; index < len(command.Args); {
		argument := command.Args[index]
		if !argument.Resolvable {
			return "", nil, effectiveCwd, false
		}
		if argument.Value == "-C" {
			if index+1 >= len(command.Args) || !command.Args[index+1].Resolvable {
				return "", nil, effectiveCwd, false
			}
			effectiveCwd = resolvePath(command.Args[index+1].Value, effectiveCwd)
			if effectiveCwd == "" {
				return "", nil, effectiveCwd, false
			}
			index += 2
			continue
		}
		if gitGlobalValueFlag(argument.Value) {
			index += 2
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			index++
			continue
		}
		return gitSubcommandName(argument.Value), command.Args[index+1:], effectiveCwd, true
	}
	return "", nil, effectiveCwd, false
}

func gitGlobalValueFlag(flag string) bool {
	return flag == "-c" || flag == "--git-dir" || flag == "--work-tree" || flag == "--namespace"
}

func isGitRead(subcommand gitSubcommandName, args []shelldecomp.Word) bool {
	switch subcommand {
	case gitSubcommandBranch:
		_, move := branchFlagMoveTargets(args)
		return !move
	case gitSubcommandStatus, gitSubcommandLog, gitSubcommandDiff, gitSubcommandShow,
		gitSubcommandFetch, gitSubcommandRevParse:
		return true
	case gitSubcommandAdd, gitSubcommandApply, gitSubcommandCheckout, gitSubcommandCherryPick,
		gitSubcommandClean, gitSubcommandCommit, gitSubcommandMerge, gitSubcommandMV,
		gitSubcommandPush, gitSubcommandRebase, gitSubcommandReset, gitSubcommandRestore,
		gitSubcommandRevert, gitSubcommandRM, gitSubcommandStash, gitSubcommandSwitch,
		gitSubcommandTag, gitSubcommandUpdateRef, gitSubcommandAM:
		return false
	default:
		return false
	}
}

func currentContext(cwd string, state State) (string, string) {
	if worktree, found := worktreeForPath(cwd, state); found {
		return worktree.Path, worktree.Branch
	}
	return state.CurrentWorktree, state.CurrentBranch
}

func gitMutationAndRefs(
	subcommand gitSubcommandName,
	args []shelldecomp.Word,
	currentBranch string,
) (bool, []string, bool) {
	unknownRef := false
	refTargets := make([]string, 0)
	mutation := simpleGitMutation(subcommand)
	switch subcommand {
	case gitSubcommandRestore:
		mutation = true
	case gitSubcommandCheckout:
		mutation = true
	case gitSubcommandSwitch:
		mutation = true
	case gitSubcommandBranch:
		targets, branchMoves := branchFlagMoveTargets(args)
		mutation = branchMoves
		refTargets = append(refTargets, targets...)
	case gitSubcommandTag:
		mutation = hasGitFlag(args, "-f", "--force")
	case gitSubcommandUpdateRef:
		mutation = true
		targets, found, unknown := firstRefTarget(args)
		refTargets = append(refTargets, targets...)
		unknownRef = unknownRef || unknown || !found
	case gitSubcommandPush:
		targets, force, localRemote, unknown := pushTargets(args)
		refTargets = append(refTargets, targets...)
		mutation = mutation || localRemote && len(targets) > 0
		unknownRef = unknownRef || unknown
		if force {
			mutation = true
		}
	case gitSubcommandReset:
		if hasGitFlag(args, "--hard") {
			refTargets = append(refTargets, currentBranch)
		}
	case gitSubcommandAdd, gitSubcommandAM, gitSubcommandApply, gitSubcommandCherryPick,
		gitSubcommandClean, gitSubcommandCommit, gitSubcommandDiff, gitSubcommandFetch,
		gitSubcommandLog, gitSubcommandMerge, gitSubcommandMV, gitSubcommandRebase,
		gitSubcommandRevParse, gitSubcommandRevert, gitSubcommandRM, gitSubcommandShow,
		gitSubcommandStash, gitSubcommandStatus:
	}
	for _, target := range refTargets {
		if strings.Contains(target, "$") || containsDynamicCommand(target) {
			unknownRef = true
		}
	}
	return mutation, refTargets, unknownRef
}

func simpleGitMutation(subcommand gitSubcommandName) bool {
	switch subcommand {
	case gitSubcommandCommit, gitSubcommandAdd, gitSubcommandRM, gitSubcommandMV,
		gitSubcommandReset, gitSubcommandStash, gitSubcommandMerge, gitSubcommandRebase,
		gitSubcommandCherryPick, gitSubcommandApply, gitSubcommandAM, gitSubcommandClean,
		gitSubcommandRevert:
		return true
	case gitSubcommandBranch, gitSubcommandCheckout, gitSubcommandDiff, gitSubcommandFetch,
		gitSubcommandLog, gitSubcommandPush, gitSubcommandRestore, gitSubcommandRevParse,
		gitSubcommandShow, gitSubcommandStatus, gitSubcommandSwitch, gitSubcommandTag,
		gitSubcommandUpdateRef:
		return false
	default:
		return false
	}
}

type gitSubcommandName string

const (
	gitSubcommandAdd        gitSubcommandName = "add"
	gitSubcommandAM         gitSubcommandName = "am"
	gitSubcommandApply      gitSubcommandName = "apply"
	gitSubcommandBranch     gitSubcommandName = "branch"
	gitSubcommandCheckout   gitSubcommandName = "checkout"
	gitSubcommandCherryPick gitSubcommandName = "cherry-pick"
	gitSubcommandClean      gitSubcommandName = "clean"
	gitSubcommandCommit     gitSubcommandName = "commit"
	gitSubcommandDiff       gitSubcommandName = "diff"
	gitSubcommandFetch      gitSubcommandName = "fetch"
	gitSubcommandLog        gitSubcommandName = "log"
	gitSubcommandMerge      gitSubcommandName = "merge"
	gitSubcommandMV         gitSubcommandName = "mv"
	gitSubcommandPush       gitSubcommandName = "push"
	gitSubcommandRebase     gitSubcommandName = "rebase"
	gitSubcommandReset      gitSubcommandName = "reset"
	gitSubcommandRestore    gitSubcommandName = "restore"
	gitSubcommandRevert     gitSubcommandName = "revert"
	gitSubcommandRevParse   gitSubcommandName = "rev-parse"
	gitSubcommandRM         gitSubcommandName = "rm"
	gitSubcommandShow       gitSubcommandName = "show"
	gitSubcommandStash      gitSubcommandName = "stash"
	gitSubcommandStatus     gitSubcommandName = "status"
	gitSubcommandSwitch     gitSubcommandName = "switch"
	gitSubcommandTag        gitSubcommandName = "tag"
	gitSubcommandUpdateRef  gitSubcommandName = "update-ref"
)

func hasGitFlag(args []shelldecomp.Word, flags ...string) bool {
	for _, argument := range args {
		for _, flag := range flags {
			if argument.Value == flag || argument.Text == flag {
				return true
			}
		}
	}
	return false
}

func branchFlagMoveTargets(args []shelldecomp.Word) ([]string, bool) {
	hasMoveFlag := false
	for _, argument := range args {
		if argument.Value == "-f" || argument.Value == "-F" || argument.Value == "--force" ||
			argument.Value == "-D" || argument.Value == "--delete" {
			hasMoveFlag = true
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			continue
		}
		if hasMoveFlag {
			if !argument.Resolvable {
				return nil, true
			}
			return []string{argument.Value}, true
		}
	}
	return nil, hasMoveFlag
}

func firstRefTarget(args []shelldecomp.Word) ([]string, bool, bool) {
	skipNext := false
	for _, argument := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if argument.Value == "-m" {
			skipNext = true
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			continue
		}
		if !argument.Resolvable {
			return nil, true, true
		}
		return []string{argument.Value}, true, false
	}
	return nil, false, false
}

func pushTargets(args []shelldecomp.Word) ([]string, bool, bool, bool) {
	force := false
	unknown := false
	positionals := make([]string, 0, len(args))
	for _, argument := range args {
		if argument.Value == "--force" || argument.Value == "-f" ||
			argument.Value == "--force-with-lease" {
			force = true
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			continue
		}
		if !argument.Resolvable {
			unknown = true
			continue
		}
		positionals = append(positionals, argument.Value)
	}
	if len(positionals) == 0 {
		return nil, force, false, unknown
	}
	remote := positionals[0]
	localRemote := remote == "." || strings.HasPrefix(remote, "/") ||
		strings.HasPrefix(remote, "file://")
	targets := make([]string, 0, len(positionals)-1)
	for _, refspec := range positionals[1:] {
		if strings.HasPrefix(refspec, "+") {
			force = true
		}
		_, destination, found := strings.Cut(refspec, ":")
		if !found {
			continue
		}
		targets = append(targets, destination)
	}
	return targets, force, localRemote, unknown
}

func branchFromRef(ref string) string {
	cleaned := strings.TrimPrefix(ref, "+")
	if cleaned == "" || cleaned == ":" {
		return ""
	}
	if after, found := strings.CutPrefix(cleaned, "refs/heads/"); found {
		return after
	}
	if after, found := strings.CutPrefix(cleaned, "heads/"); found {
		return after
	}
	if strings.HasPrefix(cleaned, "refs/") {
		return ""
	}
	return branchName(cleaned)
}

func otherWorktreeBranches(currentWorktree string, state State) map[string]struct{} {
	branches := make(map[string]struct{})
	for _, worktree := range state.Worktrees {
		if worktree.Path == "" || worktree.Branch == "" || samePath(worktree.Path, currentWorktree) {
			continue
		}
		branches[worktree.Branch] = struct{}{}
	}
	return branches
}

func worktreeForPath(candidate string, state State) (WorktreeEntry, bool) {
	cleanCandidate := cleanPath(candidate)
	var best WorktreeEntry
	found := false
	for _, worktree := range state.Worktrees {
		if worktree.Path == "" || !isUnderPath(cleanCandidate, worktree.Path) {
			continue
		}
		if !found || len(worktree.Path) > len(best.Path) {
			best = worktree
			found = true
		}
	}
	return best, found
}

func isUnderPath(candidate, root string) bool {
	cleanCandidate := cleanPath(candidate)
	cleanRoot := cleanPath(root)
	return cleanCandidate == cleanRoot || strings.HasPrefix(cleanCandidate, cleanRoot+string(filepath.Separator))
}

func samePath(left, right string) bool {
	return cleanPath(left) == cleanPath(right)
}
