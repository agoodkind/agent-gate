package rules

import (
	"strings"

	"goodkind.io/gksyntax/shelldecomp"
)

func normalizeResetBranchArgs(
	args []shelldecomp.Word,
	flag string,
) []shelldecomp.Word {
	if flag != "C" {
		return args
	}
	normalized := append([]shelldecomp.Word(nil), args...)
	for index := range normalized {
		if normalized[index].Value == "--force-create" {
			normalized[index].Value = "-C"
			continue
		}
		if target, found := strings.CutPrefix(
			normalized[index].Value, "--force-create=",
		); found {
			normalized[index].Value = "-C" + target
		}
	}
	return normalized
}
