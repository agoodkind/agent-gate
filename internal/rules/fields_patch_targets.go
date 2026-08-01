package rules

import (
	"path/filepath"
	"strings"
)

// patchFileDirectives are the apply_patch header lines that name a file the
// patch writes. Codex emits them inside the command body, so a rule keyed on
// tool_input.file_path sees nothing and every path-based write rule is blind to
// the write. Measured on 2026-08-01: of 16,149 recorded apply_patch events, 2
// carried a non-empty file_path.
var patchFileDirectives = []string{
	"*** Update File:",
	"*** Add File:",
	"*** Delete File:",
	"*** Move to:",
}

// PatchWriteTargets returns the newline-joined absolute paths a patch-format
// tool call writes, resolved against the working directory.
//
// It reads the patch body rather than a path field, which is the only place
// apply_patch states its targets. A payload that is not a patch yields nothing,
// so declaring this selector on a rule costs nothing for other tools.
func (fields FieldSet) PatchWriteTargets() string {
	body := fields.ToolInputCommand
	if body == "" {
		body = fields.ToolInputContent
	}
	if !strings.Contains(body, "*** Begin Patch") {
		return ""
	}

	base := fields.BaseCWD()
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, directive := range patchFileDirectives {
			rest, found := strings.CutPrefix(trimmed, directive)
			if !found {
				continue
			}
			target := strings.TrimSpace(rest)
			if target == "" {
				continue
			}
			if !filepath.IsAbs(target) {
				if base == "" {
					// Without a working directory a relative target cannot be
					// resolved, and guessing one would fabricate a path that a
					// rule might then match or clear. Drop it instead.
					continue
				}
				target = filepath.Join(base, target)
			}
			if _, duplicate := seen[target]; duplicate {
				continue
			}
			seen[target] = struct{}{}
			paths = append(paths, target)
		}
	}
	return strings.Join(paths, "\n")
}
