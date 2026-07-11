package rules

import (
	"slices"
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

var gitContinuingGlobalOptions = []string{
	"-p",
	"--paginate",
	"-P",
	"--no-pager",
	"--no-replace-objects",
	"--no-lazy-fetch",
	"--no-optional-locks",
	"--no-advice",
	"--bare",
	"--literal-pathspecs",
	"--glob-pathspecs",
	"--noglob-pathspecs",
	"--icase-pathspecs",
}

var gitTerminalGlobalOptions = []string{
	"-v",
	"--version",
	"-h",
	"--help",
	"--html-path",
	"--man-path",
	"--info-path",
	"--exec-path",
}

var gitRequiredValueGlobalOptions = []string{
	"--namespace",
	"--super-prefix",
	"--config-env",
	"--attr-source",
}

func consumeGitGlobalOption(
	words []shelldecomp.Word,
	index int,
) (int, bool, bool) {
	value := words[index].Value
	if slices.Contains(gitContinuingGlobalOptions, value) {
		return index, true, true
	}
	if slices.Contains(gitTerminalGlobalOptions, value) {
		return index, true, false
	}
	if inlineValue, found := strings.CutPrefix(value, "--exec-path="); found {
		return index, true, inlineValue != ""
	}
	if value == "-c" {
		return consumeSeparateGitGlobalValue(words, index)
	}
	for _, name := range gitRequiredValueGlobalOptions {
		if value == name {
			return consumeSeparateGitGlobalValue(words, index)
		}
		if inlineValue, found := strings.CutPrefix(value, name+"="); found {
			return index, true, inlineValue != ""
		}
	}
	if strings.HasPrefix(value, "-") {
		return index, true, false
	}
	return index, false, false
}

func consumeSeparateGitGlobalValue(
	words []shelldecomp.Word,
	index int,
) (int, bool, bool) {
	if index+1 >= len(words) || !words[index+1].Resolvable || words[index+1].Value == "" {
		return index, true, false
	}
	return index + 1, true, true
}
