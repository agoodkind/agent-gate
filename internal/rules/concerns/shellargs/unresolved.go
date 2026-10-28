package shellargs

import (
	"slices"
	"strings"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/gksyntax/shelldecomp"
)

func (state *parser) unresolvedAttached(word shelldecomp.Word) bool {
	raw := word.Text
	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "'") {
		raw = raw[1:]
	}
	prefix := raw
	if boundary := strings.IndexAny(prefix, "\"'\\$`"); boundary >= 0 {
		prefix = prefix[:boundary]
	}
	if !strings.HasPrefix(prefix, "-") || len(prefix) < 2 {
		return false
	}
	for _, option := range state.spec.Options {
		for _, name := range option.Names {
			if strings.HasPrefix(prefix, name+"=") && slices.Contains(option.Forms, "equals") {
				state.record(option, unresolvedValue(raw[len(name)+1:]))
				return true
			}
		}
	}
	if option, width, found := state.unresolvedAttachedOption(prefix); found {
		state.record(option, unresolvedValue(raw[width:]))
		return true
	}
	if !state.spec.ShortClusters || strings.HasPrefix(prefix, "--") {
		return false
	}
	return state.unresolvedCluster(prefix[1:], raw)
}

func (state *parser) unresolvedAttachedOption(prefix string) (config.ArgumentOptionSpec, int, bool) {
	var selected config.ArgumentOptionSpec
	width := 0
	for _, option := range state.spec.Options {
		if !independentAttachedValue(option) {
			continue
		}
		for _, name := range option.Names {
			if len(name) > width && strings.HasPrefix(prefix, name) {
				selected = option
				width = len(name)
			}
		}
	}
	return selected, width, width > 0
}

func (state *parser) unresolvedCluster(prefix, raw string) bool {
	for index := range len(prefix) {
		option, found := state.namedOption("-" + prefix[index:index+1])
		if !found {
			return false
		}
		if option.Value == "none" {
			state.record(option, literal(""))
			continue
		}
		if !independentAttachedValue(option) {
			return false
		}
		state.record(option, unresolvedValue(raw[index+2:]))
		return true
	}
	return false
}

func independentAttachedValue(option config.ArgumentOptionSpec) bool {
	return option.Value != "none" &&
		option.AttachedValuePattern == "" &&
		slices.Contains(option.Forms, "attached") &&
		!slices.Contains(option.Forms, "separate")
}

func unresolvedValue(raw string) shelldecomp.Word {
	return shelldecomp.Word{Text: raw, Value: raw, Resolvable: false, Kind: shelldecomp.WordKindUnresolvable}
}
