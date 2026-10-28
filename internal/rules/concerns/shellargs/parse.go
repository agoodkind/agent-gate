// Package shellargs interprets command arguments using configuration descriptions.
package shellargs

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/gksyntax/shelldecomp"
)

// Result reports argument facts without selecting an enforcement action.
type Result struct {
	Operands             []shelldecomp.Word
	Options              map[string][]shelldecomp.Word
	CWD                  string
	Selected             bool
	Matched              bool
	Unresolved           bool
	ActivationUnresolved bool
}

type parser struct {
	spec            config.ArgumentSpec
	result          Result
	optionsEnded    bool
	operandsStopped bool
	subcommandIndex int
}

// Parse interprets the words of one already parsed shell command.
func Parse(command shelldecomp.Command, spec config.ArgumentSpec) Result {
	var state parser
	state.spec = spec
	state.result.CWD = command.Cwd
	state.result.Selected = true
	state.result.Matched = len(spec.Subcommands) == 0
	state.result.Options = make(map[string][]shelldecomp.Word)
	for index := 0; index < len(command.Args); index++ {
		word := command.Args[index]
		if !state.optionsEnded && !word.Resolvable && state.unresolvedAttached(word) {
			if state.spec.OperandsBeforeOptions {
				state.operandsStopped = true
			}
			continue
		}
		if !state.optionsEnded && !word.Resolvable && (state.spec.UnknownOptions != "skip" || strings.HasPrefix(strings.TrimLeft(word.Text, "\"'"), "-")) {
			state.result.Unresolved = true
			state.finish(true)
			return state.result
		}
		if state.spec.EndOfOptions && !state.optionsEnded && word.Resolvable && word.Value == "--" {
			state.optionsEnded = true
			continue
		}
		if !state.optionsEnded && word.Resolvable && strings.HasPrefix(word.Value, "-") && word.Value != "-" {
			if state.spec.OperandsBeforeOptions {
				state.operandsStopped = true
			}
			consumed, resolved := state.option(word, command.Args[index+1:])
			if !resolved {
				state.result.Unresolved = true
				state.finish(true)
				return state.result
			}
			index += consumed
			continue
		}
		if !state.positional(word) {
			return state.result
		}
	}
	state.finish(false)
	return state.result
}

func (state *parser) positional(word shelldecomp.Word) bool {
	if state.subcommandIndex < len(state.spec.Subcommands) {
		if !word.Resolvable {
			state.result.Unresolved = true
			return false
		}
		if word.Value != state.spec.Subcommands[state.subcommandIndex] {
			state.result.Selected = false
			return false
		}
		state.subcommandIndex++
		state.result.Matched = state.subcommandIndex == len(state.spec.Subcommands)
		return true
	}
	if word.Resolvable && slices.Contains(state.spec.OperandStopTokens, word.Value) {
		state.operandsStopped = true
		return true
	}
	if state.operandsStopped {
		return true
	}
	if state.spec.StopAtFirstPositional {
		state.optionsEnded = true
	}
	for _, prefix := range state.spec.IgnoreOperandPrefixes {
		if word.Resolvable && strings.HasPrefix(word.Value, prefix) {
			return true
		}
	}
	state.result.Operands = append(state.result.Operands, word)
	return true
}

func (state *parser) option(word shelldecomp.Word, following []shelldecomp.Word) (int, bool) {
	for _, option := range state.spec.Options {
		for _, name := range option.Names {
			if word.Value == name {
				return state.consume(option, "", false, following)
			}
			if value, found := strings.CutPrefix(word.Value, name+"="); found && slices.Contains(option.Forms, "equals") {
				return state.consume(option, value, true, following)
			}
		}
	}
	if option, value, found := state.attachedOption(word.Value); found {
		return state.consume(option, value, true, following)
	}
	if state.spec.ShortClusters && len(word.Value) > 2 && word.Value[1] != '-' {
		return state.cluster(word.Value[1:], following)
	}
	return 0, state.spec.UnknownOptions == "skip"
}

func (state *parser) attachedOption(token string) (config.ArgumentOptionSpec, string, bool) {
	var selected config.ArgumentOptionSpec
	longest := 0
	value := ""
	for _, option := range state.spec.Options {
		if !slices.Contains(option.Forms, "attached") {
			continue
		}
		for _, name := range option.Names {
			if state.spec.ShortClusters && len(name) == 2 && option.AttachedValuePattern != "" {
				continue
			}
			if len(name) <= longest || !strings.HasPrefix(token, name) || len(token) == len(name) {
				continue
			}
			selected = option
			longest = len(name)
			value = token[len(name):]
		}
	}
	return selected, value, longest > 0
}

func (state *parser) cluster(cluster string, following []shelldecomp.Word) (int, bool) {
	for cluster != "" {
		option, found := state.namedOption("-" + cluster[:1])
		cluster = cluster[1:]
		if !found {
			if state.spec.UnknownOptions == "skip" {
				continue
			}
			return 0, false
		}
		if option.Value == "none" {
			state.record(option, literal(""))
			continue
		}
		if option.AttachedValuePattern != "" {
			if cluster == "" {
				return state.consume(option, "", false, following)
			}
			consumed, valid := state.clusterValuePrefix(option, cluster)
			if !valid {
				return 0, false
			}
			cluster = cluster[consumed:]
			continue
		}
		if cluster != "" {
			if !slices.Contains(option.Forms, "attached") {
				return 0, false
			}
			return state.consume(option, cluster, true, following)
		}
		return state.consume(option, "", false, following)
	}
	return 0, true
}

func (state *parser) clusterValuePrefix(option config.ArgumentOptionSpec, suffix string) (int, bool) {
	pattern, err := regexp.Compile(option.AttachedValuePattern)
	if err != nil {
		return 0, false
	}
	span := pattern.FindStringIndex(suffix)
	if span == nil || span[0] != 0 || span[1] == 0 && option.Value == "required" {
		if option.Value == "optional" {
			state.record(option, literal(""))
			return 0, true
		}
		return 0, false
	}
	state.record(option, literal(suffix[:span[1]]))
	return span[1], true
}

func (state *parser) namedOption(name string) (config.ArgumentOptionSpec, bool) {
	for _, option := range state.spec.Options {
		if slices.Contains(option.Names, name) {
			return option, true
		}
	}
	var empty config.ArgumentOptionSpec
	return empty, false
}

func (state *parser) consume(option config.ArgumentOptionSpec, attached string, hasAttached bool, following []shelldecomp.Word) (int, bool) {
	if option.Value == "none" {
		if hasAttached {
			return 0, false
		}
		state.record(option, literal(""))
		return 0, true
	}
	if hasAttached {
		if option.AttachedValuePattern != "" && !matchesAttachedValue(option.AttachedValuePattern, attached) {
			return 0, false
		}
		state.record(option, literal(attached))
		return 0, true
	}
	if slices.Contains(option.Forms, "separate") && len(following) > 0 {
		state.record(option, following[0])
		return 1, true
	}
	if option.Value == "optional" {
		state.record(option, literal(""))
		return 0, true
	}
	return 0, false
}

func matchesAttachedValue(expression, value string) bool {
	pattern, err := regexp.Compile(expression)
	if err != nil {
		return false
	}
	span := pattern.FindStringIndex(value)
	return span != nil && span[0] == 0 && span[1] == len(value)
}

func (state *parser) record(option config.ArgumentOptionSpec, value shelldecomp.Word) {
	for _, name := range option.Names {
		state.result.Options[name] = append(state.result.Options[name], value)
	}
	if option.CWD {
		if !value.Resolvable || value.Value == "" {
			state.result.CWD = shelldecomp.Unresolvable
			state.result.Unresolved = true
			return
		}
		state.result.CWD = ResolvePath(value.Value, state.result.CWD)
	}
}

func (state *parser) finish(partial bool) {
	if state.subcommandIndex != len(state.spec.Subcommands) {
		state.result.Selected = false
		return
	}
	for _, name := range state.spec.RequireOptions {
		if len(state.result.Options[name]) == 0 {
			if partial {
				state.result.Operands = nil
				state.result.ActivationUnresolved = true
			} else {
				state.result.Selected = false
			}
		}
	}
	state.applyPredicates(state.spec.RequireOptionValues, true, partial)
	state.applyPredicates(state.spec.ExcludeOptionValues, false, partial)
	skip := state.spec.SkipPositionals
	for _, name := range state.spec.ProgramOptions {
		if len(state.result.Options[name]) > 0 {
			skip = max(0, skip-1)
			break
		}
	}
	state.result.Operands = state.result.Operands[min(skip, len(state.result.Operands)):]
}

func (state *parser) applyPredicates(predicates []config.ArgumentOptionPredicate, required, partial bool) {
	for _, predicate := range predicates {
		matched, unresolved := matchesPredicate(state.result.Options, predicate)
		if required && partial && !matched {
			unresolved = true
		}
		if unresolved && !matched {
			state.result.Unresolved = true
			state.result.Operands = nil
			state.result.ActivationUnresolved = true
			continue
		}
		if matched != required {
			state.result.Selected = false
		}
	}
}

func matchesPredicate(options map[string][]shelldecomp.Word, predicate config.ArgumentOptionPredicate) (bool, bool) {
	unresolved := false
	for _, value := range options[predicate.Option] {
		if !value.Resolvable {
			unresolved = true
			continue
		}
		if slices.Contains(predicate.Values, value.Value) {
			return true, false
		}
	}
	return false, unresolved
}

func literal(value string) shelldecomp.Word {
	return shelldecomp.Word{Text: value, Value: value, Resolvable: true, Kind: shelldecomp.WordKindLiteral}
}

// ResolvePath resolves a literal path against a known starting directory.
func ResolvePath(value, cwd string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	if cwd == "" || cwd == shelldecomp.Unresolvable {
		return ""
	}
	return filepath.Clean(filepath.Join(cwd, value))
}
