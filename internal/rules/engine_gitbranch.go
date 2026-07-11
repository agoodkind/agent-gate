package rules

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
	"goodkind.io/agent-gate/internal/rules/concerns/shellparse"
	"goodkind.io/gksyntax/shelldecomp"
)

type gitStateReader func(string) (gitbranch.State, error)

type gitRefSubcommand string

type branchMoveMode int

type pushBooleanEffect int

type pushBooleanOption struct {
	longName  string
	shortName rune
	negatable bool
	effect    pushBooleanEffect
}

type pushValueOption struct {
	longName      string
	shortName     rune
	allowBare     bool
	allowSeparate bool
	allowInline   bool
	negatable     bool
	allowedValues []string
}

const (
	gitRefSubcommandBranch    gitRefSubcommand = "branch"
	gitRefSubcommandCheckout  gitRefSubcommand = "checkout"
	gitRefSubcommandPush      gitRefSubcommand = "push"
	gitRefSubcommandSwitch    gitRefSubcommand = "switch"
	gitRefSubcommandUpdateRef gitRefSubcommand = "update-ref"
)

const (
	pushEffectNone pushBooleanEffect = iota
	pushEffectDelete
	pushEffectDryRun
)

var pushBooleanOptions = []pushBooleanOption{
	{longName: "all", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "atomic", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "branches", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "delete", shortName: 'd', negatable: true, effect: pushEffectDelete},
	{longName: "dry-run", shortName: 'n', negatable: true, effect: pushEffectDryRun},
	{longName: "follow-tags", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "force", shortName: 'f', negatable: true, effect: pushEffectNone},
	{longName: "force-if-includes", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "ipv4", shortName: '4', negatable: false, effect: pushEffectNone},
	{longName: "ipv6", shortName: '6', negatable: false, effect: pushEffectNone},
	{longName: "mirror", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "porcelain", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "progress", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "prune", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "quiet", shortName: 'q', negatable: true, effect: pushEffectNone},
	{longName: "set-upstream", shortName: 'u', negatable: true, effect: pushEffectNone},
	{longName: "tags", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "thin", shortName: 0, negatable: true, effect: pushEffectNone},
	{longName: "verbose", shortName: 'v', negatable: true, effect: pushEffectNone},
	{longName: "verify", shortName: 0, negatable: true, effect: pushEffectNone},
}

var pushValueOptions = []pushValueOption{
	{longName: "exec", shortName: 0, allowBare: false, allowSeparate: true, allowInline: true, negatable: false, allowedValues: nil},
	{longName: "force-with-lease", shortName: 0, allowBare: true, allowSeparate: false, allowInline: true, negatable: true, allowedValues: nil},
	{longName: "push-option", shortName: 'o', allowBare: false, allowSeparate: true, allowInline: true, negatable: false, allowedValues: nil},
	{longName: "receive-pack", shortName: 0, allowBare: false, allowSeparate: true, allowInline: true, negatable: false, allowedValues: nil},
	{longName: "recurse-submodules", shortName: 0, allowBare: false, allowSeparate: true, allowInline: true, negatable: true, allowedValues: []string{"check", "on-demand", "no"}},
	{longName: "signed", shortName: 0, allowBare: true, allowSeparate: false, allowInline: true, negatable: true, allowedValues: []string{"yes", "no", "if-asked"}},
}

const (
	branchMoveNone branchMoveMode = iota
	branchMoveForce
	branchMoveDelete
	branchMoveRename
	branchMoveCopy
)

// gitDefaultBranchConditionMatch reports whether any target the operation acts
// on lives in a git repo whose HEAD is the default branch. Targets come from the
// resolved command cwds when a command condition is present (a git verb's
// -C/cd/process-cwd repo), otherwise from the condition's field selectors (an
// edit's file_path, or cmd_write_targets for a shell write). This mirrors how
// projectConditionMatch sources its directories, so the decision is the branch
// of the affected repo, never the shell's cwd shape. A detached or unresolved
// repo never matches, so a block built on this condition fails open. allConditionsMatch
// evaluates conditions in config order, so place this condition after the cheaper
// gate (an edit-tool regex or a git command condition) in the rule; that preceding
// gate then short-circuits and the go-git open runs on candidate events only.
func gitDefaultBranchConditionMatch(fields FieldSet, c *config.Condition, ctx conditionContext) bool {
	for _, target := range gitBranchTargets(fields, c, ctx) {
		if match, resolved := gitbranch.OnDefaultBranch(target); resolved && match {
			return true
		}
	}
	return false
}

func gitPrimaryCheckoutConditionMatch(
	fields FieldSet,
	c *config.Condition,
	ctx conditionContext,
	readState gitStateReader,
) bool {
	for _, target := range gitBranchTargets(fields, c, ctx) {
		state, err := readState(target)
		if err == nil && gitbranch.IsPrimaryCheckout(state, target) {
			return true
		}
	}
	return false
}

func gitConditionMatch(
	fields FieldSet,
	c *config.Condition,
	ctx conditionContext,
	readState gitStateReader,
) bool {
	switch config.ConditionKind(c.Kind) {
	case config.ConditionKindGitDefaultBranch:
		return gitDefaultBranchConditionMatch(fields, c, ctx)
	case config.ConditionKindGitPrimaryCheckout:
		return gitPrimaryCheckoutConditionMatch(fields, c, ctx, readState)
	case config.ConditionKindGitRefMove:
		return gitRefMoveConditionMatch(fields, readState)
	case config.ConditionKindCommand, config.ConditionKindDiff, config.ConditionKindExec,
		config.ConditionKindProject, config.ConditionKindRegex, config.ConditionKindShellRead,
		config.ConditionKindShellWrite, config.ConditionKindComposer:
		return false
	}
	return false
}

func gitRefMoveConditionMatch(fields FieldSet, readState gitStateReader) bool {
	commandText := fields.CommandValue()
	base := fields.BaseCWD()
	if commandText == "" || base == "" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = base
	}
	decomposition := shelldecomp.Parse(
		shellparse.ExpandLiteralAssignments(commandText),
		base,
		home,
	)
	if decomposition.IsOpaque() {
		return false
	}
	for _, parsed := range decomposition.Commands() {
		argv0 := filepath.Base(parsed.Argv0)
		argv0, words := stripCommandWrappers(
			argv0,
			parsed.Args,
			[]string{"command", "doas", "exec", "time"},
		)
		if argv0 != "git" {
			continue
		}
		cwd := parsed.Cwd
		if cwd == "" {
			cwd = base
		}
		subcommand, args, cwd, resolved := gitInvocation(words, cwd)
		if !resolved {
			continue
		}
		branches, statePath := movedLocalBranches(subcommand, args, cwd)
		if len(branches) == 0 || statePath == "" {
			continue
		}
		state, readErr := readState(statePath)
		if readErr != nil {
			continue
		}
		for _, branch := range branches {
			if gitbranch.BranchCheckedOutElsewhere(state, cwd, branch) {
				return true
			}
		}
	}
	return false
}

func gitInvocation(
	words []shelldecomp.Word,
	cwd string,
) (string, []shelldecomp.Word, string, bool) {
	for index := 0; index < len(words); index++ {
		word := words[index]
		if !word.Resolvable {
			return "", nil, "", false
		}
		if word.Value == "-C" {
			if index+1 >= len(words) || !words[index+1].Resolvable {
				return "", nil, "", false
			}
			cwd = resolvePath(cwd, words[index+1].Value)
			index++
			continue
		}
		if gitGlobalValueFlags[word.Value] {
			if index+1 >= len(words) || !words[index+1].Resolvable {
				return "", nil, "", false
			}
			index++
			continue
		}
		if word.Kind == shelldecomp.WordKindFlag {
			continue
		}
		return word.Value, words[index+1:], cwd, true
	}
	return "", nil, "", false
}

func movedLocalBranches(
	subcommand string,
	args []shelldecomp.Word,
	cwd string,
) ([]string, string) {
	switch gitRefSubcommand(subcommand) {
	case gitRefSubcommandBranch:
		return branchMoveTargets(args), cwd
	case gitRefSubcommandUpdateRef:
		return updateRefTargets(args), cwd
	case gitRefSubcommandCheckout:
		return resetBranchTarget(args, "B"), cwd
	case gitRefSubcommandSwitch:
		return resetBranchTarget(args, "C"), cwd
	case gitRefSubcommandPush:
		return localPushTargets(args, cwd)
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
		if len(positionals) < 1 || len(positionals) > 2 {
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
		if len(positionals) != 2 {
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

func updateRefTargets(args []shelldecomp.Word) []string {
	deleteRef := false
	positionals := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if !argument.Resolvable {
			return nil
		}
		if argument.Value == "-m" {
			if index+1 >= len(args) || !args[index+1].Resolvable {
				return nil
			}
			index++
			continue
		}
		if argument.Value == "--stdin" {
			return nil
		}
		if argument.Value == "-d" {
			deleteRef = true
			continue
		}
		if argument.Value == "--no-deref" || argument.Value == "--create-reflog" {
			continue
		}
		if strings.HasPrefix(argument.Value, "-") {
			return nil
		}
		positionals = append(positionals, argument.Value)
	}
	if deleteRef {
		if len(positionals) < 1 || len(positionals) > 2 {
			return nil
		}
	} else if len(positionals) < 2 || len(positionals) > 3 {
		return nil
	}
	return localBranchTarget(positionals[0])
}

func resetBranchTarget(args []shelldecomp.Word, flag string) []string {
	for index, argument := range args {
		if !argument.Resolvable {
			return nil
		}
		if argument.Value == "-"+flag {
			if index+1 >= len(args) || !args[index+1].Resolvable {
				return nil
			}
			return localBranchTarget(args[index+1].Value)
		}
		if target, found := strings.CutPrefix(argument.Value, "-"+flag); found && target != "" {
			return localBranchTarget(target)
		}
	}
	return nil
}

func localPushTargets(args []shelldecomp.Word, cwd string) ([]string, string) {
	repository, refspecs, deleteRefs, dryRun, valid := parseLocalPushArgs(args)
	if !valid || dryRun {
		return nil, ""
	}
	statePath := localPushRepository(repository, cwd)
	if statePath == "" {
		return nil, ""
	}
	branches := pushRefspecBranches(refspecs, deleteRefs)
	return branches, statePath
}

func parseLocalPushArgs(args []shelldecomp.Word) (string, []string, bool, bool, bool) {
	state := localPushParseState{
		repository:  "",
		positionals: make([]string, 0, len(args)),
		deleteRefs:  false,
		dryRun:      false,
		endOptions:  false,
	}
	for index := 0; index < len(args); index++ {
		if !args[index].Resolvable {
			return "", nil, false, false, false
		}
		nextIndex, valid := consumeLocalPushArgument(args, index, &state)
		if !valid {
			return "", nil, false, false, false
		}
		index = nextIndex
	}
	return finishLocalPushArgs(state)
}

type localPushParseState struct {
	repository  string
	positionals []string
	deleteRefs  bool
	dryRun      bool
	endOptions  bool
}

func consumeLocalPushArgument(
	args []shelldecomp.Word,
	index int,
	state *localPushParseState,
) (int, bool) {
	value := args[index].Value
	if state.endOptions {
		state.positionals = append(state.positionals, value)
		return index, true
	}
	if value == "--" {
		state.endOptions = true
		return index, true
	}
	if nextIndex, recognized, valid := consumePushValueOption(args, index); recognized {
		return nextIndex, valid
	}
	if value == "--repo" {
		if state.repository != "" || index+1 >= len(args) || !args[index+1].Resolvable {
			return index, false
		}
		state.repository = args[index+1].Value
		return index + 1, true
	}
	if inlineRepository, found := strings.CutPrefix(value, "--repo="); found {
		if state.repository != "" || inlineRepository == "" {
			return index, false
		}
		state.repository = inlineRepository
		return index, true
	}
	if strings.HasPrefix(value, "-") && value != "-" {
		return index, consumePushBooleanOption(value, state)
	}
	state.positionals = append(state.positionals, value)
	return index, true
}

func finishLocalPushArgs(state localPushParseState) (string, []string, bool, bool, bool) {
	refspecs := state.positionals
	if state.repository == "" {
		if len(state.positionals) < 2 {
			return "", nil, false, false, false
		}
		state.repository = state.positionals[0]
		refspecs = state.positionals[1:]
	} else if len(refspecs) == 0 {
		return "", nil, false, false, false
	}
	return state.repository, refspecs, state.deleteRefs, state.dryRun, true
}

func consumePushBooleanOption(value string, state *localPushParseState) bool {
	if name, longOption := strings.CutPrefix(value, "--"); longOption {
		negated := false
		if after, found := strings.CutPrefix(name, "no-"); found {
			name = after
			negated = true
		}
		for _, option := range pushBooleanOptions {
			if option.longName != name || negated && !option.negatable {
				continue
			}
			applyPushBooleanEffect(option.effect, !negated, state)
			return true
		}
		for _, option := range pushValueOptions {
			if option.longName == name && negated && option.negatable {
				return true
			}
		}
		return false
	}
	if len(value) < 2 || value[0] != '-' {
		return false
	}
	for _, shortName := range value[1:] {
		option, found := pushBooleanByShort(shortName)
		if !found {
			return false
		}
		applyPushBooleanEffect(option.effect, true, state)
	}
	return true
}

func pushBooleanByShort(shortName rune) (pushBooleanOption, bool) {
	for _, option := range pushBooleanOptions {
		if option.shortName == shortName {
			return option, true
		}
	}
	return pushBooleanOption{longName: "", shortName: 0, negatable: false, effect: pushEffectNone}, false
}

func applyPushBooleanEffect(
	effect pushBooleanEffect,
	enabled bool,
	state *localPushParseState,
) {
	switch effect {
	case pushEffectDelete:
		state.deleteRefs = enabled
	case pushEffectDryRun:
		state.dryRun = enabled
	case pushEffectNone:
	}
}

func consumePushValueOption(
	args []shelldecomp.Word,
	index int,
) (int, bool, bool) {
	value := args[index].Value
	if strings.HasPrefix(value, "--") {
		return consumeLongPushValueOption(args, index)
	}
	if len(value) < 2 || value[0] != '-' {
		return index, false, false
	}
	for _, option := range pushValueOptions {
		if option.shortName == 0 || rune(value[1]) != option.shortName {
			continue
		}
		if len(value) > 2 {
			return index, true, optionValueAllowed(option, value[2:])
		}
		if index+1 >= len(args) || !args[index+1].Resolvable {
			return index, true, false
		}
		return index + 1, true, optionValueAllowed(option, args[index+1].Value)
	}
	return index, false, false
}

func consumeLongPushValueOption(
	args []shelldecomp.Word,
	index int,
) (int, bool, bool) {
	nameValue := strings.TrimPrefix(args[index].Value, "--")
	name, inlineValue, inline := strings.Cut(nameValue, "=")
	for _, option := range pushValueOptions {
		if option.longName != name {
			continue
		}
		if inline {
			return index, true, option.allowInline && optionValueAllowed(option, inlineValue)
		}
		if option.allowSeparate && index+1 < len(args) && args[index+1].Resolvable &&
			optionValueAllowed(option, args[index+1].Value) {
			return index + 1, true, true
		}
		return index, true, option.allowBare
	}
	return index, false, false
}

func optionValueAllowed(option pushValueOption, value string) bool {
	if value == "" {
		return false
	}
	return len(option.allowedValues) == 0 || slices.Contains(option.allowedValues, value)
}

func pushRefspecBranches(refspecs []string, deleteRefs bool) []string {
	branches := make([]string, 0, len(refspecs))
	for _, refspec := range refspecs {
		if deleteRefs {
			branches = append(branches, localBranchTarget(refspec)...)
			continue
		}
		cleaned := strings.TrimPrefix(refspec, "+")
		_, destination, found := strings.Cut(cleaned, ":")
		if !found {
			destination = cleaned
		}
		branches = append(branches, localBranchTarget(destination)...)
	}
	return branches
}

func localPushRepository(remote, cwd string) string {
	switch {
	case remote == ".":
		return cwd
	case strings.HasPrefix(remote, "file://"):
		path := strings.TrimPrefix(remote, "file://")
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return ""
	case filepath.IsAbs(remote):
		return filepath.Clean(remote)
	case strings.HasPrefix(remote, "./"), strings.HasPrefix(remote, "../"):
		return filepath.Clean(filepath.Join(cwd, remote))
	default:
		return ""
	}
}

func localBranchTarget(ref string) []string {
	ref = strings.TrimPrefix(ref, "+")
	if branch, found := strings.CutPrefix(ref, "refs/heads/"); found && branch != "" {
		return []string{branch}
	}
	if branch, found := strings.CutPrefix(ref, "heads/"); found && branch != "" {
		return []string{branch}
	}
	if ref == "" || strings.HasPrefix(ref, "refs/") || strings.ContainsRune(ref, 0) {
		return nil
	}
	return []string{ref}
}

// gitBranchTargets returns the deduplicated set of filesystem targets to test:
// the resolved command cwds (a git verb's -C/cd/process-cwd repo) merged with
// every non-empty line of every configured selector value. Both sources are
// used, so a rule that pairs a command condition with a file selector checks the
// repos of both. A relative selector value (a provider may pass a relative
// tool_input.file_path straight through) is resolved against the event cwd first,
// so a relative target is enforced rather than silently skipped.
func gitBranchTargets(fields FieldSet, c *config.Condition, ctx conditionContext) []string {
	base := fields.BaseCWD()
	targets := make([]string, 0, len(ctx.commandCwds))
	targets = append(targets, ctx.commandCwds...)
	for _, spec := range c.Selectors() {
		value := fields.StringForCondition(spec.Selector, c)
		if value == "" {
			continue
		}
		for line := range strings.SplitSeq(value, "\n") {
			if line == "" || strings.ContainsRune(line, 0) {
				continue
			}
			if !filepath.IsAbs(line) {
				if base == "" {
					continue
				}
				line = filepath.Join(base, line)
			}
			targets = append(targets, line)
		}
	}
	return dedupeUsable(targets)
}

// dedupeUsable returns the distinct usable target paths. It drops empties, the
// shelldecomp unresolvable sentinel (which carries a NUL byte), and any
// remaining non-absolute value, so an unpinnable cwd or write target can never
// collapse to "." inside gitbranch.OnDefaultBranch and accidentally evaluate the
// daemon's own directory. This preserves the fail-open contract for unresolvable
// targets while gitBranchTargets has already resolved relative selector paths.
func dedupeUsable(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || strings.ContainsRune(value, 0) || !filepath.IsAbs(value) {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
