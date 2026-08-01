package rules_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

// codexPatchBody is the shape Codex apply_patch actually sends: the targets are
// inside the command body, and no path field carries them.
const codexPatchBody = `*** Begin Patch
*** Update File: /repo/internal/config/config.go
@@
-old
+new
*** Add File: relative/new_file.go
+package main
*** Delete File: /repo/internal/gone.go
*** End Patch
`

// TestPatchWriteTargetsSeesWhatFilePathCannot is the regression for AGATE-14.
// Path-based write rules read tool_input.file_path, which apply_patch leaves
// empty, so a Codex write into a protected checkout matched no rule at all.
// Measured against the live gate on 2026-08-01: apply_patch into the primary
// checkout exited 0 with no rule matched, while the identical write through
// Edit exited 2 and matched two rules.
func TestPatchWriteTargetsSeesWhatFilePathCannot(t *testing.T) {
	fields := rules.FieldSet{
		ToolName:         "apply_patch",
		ToolInputCommand: codexPatchBody,
		CWD:              "/repo",
	}

	// The field the write rules read is empty, which is the whole defect.
	if fields.ToolInputFilePath != "" {
		t.Fatalf("apply_patch payload carried a file_path: %q", fields.ToolInputFilePath)
	}

	got := fields.PatchWriteTargets()
	for _, want := range []string{
		"/repo/internal/config/config.go",
		"/repo/relative/new_file.go",
		"/repo/internal/gone.go",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PatchWriteTargets() = %q, want it to contain %q", got, want)
		}
	}
}

// TestPatchWriteTargetsIgnoresNonPatchPayloads keeps the selector free for every
// other tool, so declaring it on a rule costs nothing elsewhere.
func TestPatchWriteTargetsIgnoresNonPatchPayloads(t *testing.T) {
	cases := []struct {
		name   string
		fields rules.FieldSet
	}{
		{"a shell command", rules.FieldSet{
			ToolInputCommand: "rm -rf /repo/internal", CWD: "/repo",
		}},
		{"an empty payload", rules.FieldSet{CWD: "/repo"}},
		{"prose that mentions a patch", rules.FieldSet{
			ToolInputCommand: "explain how to apply a patch to config.go", CWD: "/repo",
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := testCase.fields.PatchWriteTargets(); got != "" {
				t.Fatalf("PatchWriteTargets() = %q, want empty", got)
			}
		})
	}
}

// TestPatchWriteTargetsDropsUnresolvableRelativePaths keeps the selector from
// fabricating a path when there is no working directory to resolve against. A
// fabricated path could match a rule that should not fire, or miss one that
// should.
func TestPatchWriteTargetsDropsUnresolvableRelativePaths(t *testing.T) {
	fields := rules.FieldSet{
		ToolInputCommand: "*** Begin Patch\n*** Add File: relative/only.go\n*** End Patch\n",
	}
	if got := fields.PatchWriteTargets(); got != "" {
		t.Fatalf("PatchWriteTargets() = %q, want empty without a working directory", got)
	}
}
