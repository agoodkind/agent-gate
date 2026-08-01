package rules_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

// TestPatchWriteTargetsNormalizesTraversal is the regression for a rule bypass.
// An absolute directive can carry .. segments, so /repo/../etc/passwd writes
// outside /repo while still matching a rule anchored on the /repo prefix. The
// relative branch was already normalized by filepath.Join; the absolute one was
// not.
func TestPatchWriteTargetsNormalizesTraversal(t *testing.T) {
	cases := []struct {
		name      string
		directive string
		want      string
	}{
		{"absolute traversal", "*** Update File: /repo/../etc/passwd", "/etc/passwd"},
		{"absolute dot segment", "*** Update File: /repo/./a.go", "/repo/a.go"},
		{"relative traversal", "*** Update File: ../etc/passwd", "/etc/passwd"},
		{"nested traversal", "*** Add File: /repo/sub/../../etc/hosts", "/etc/hosts"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fields := rules.FieldSet{
				ToolName: "apply_patch",
				ToolInputCommand: "*** Begin Patch\n" + testCase.directive +
					"\n*** End Patch\n",
				CWD: "/repo",
			}
			if got := fields.PatchWriteTargets(); got != testCase.want {
				t.Fatalf("PatchWriteTargets() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestPatchWriteTargetsDeduplicatesSpellings keeps normalization doing the other
// half of its job: two spellings of one file are one target, not two.
func TestPatchWriteTargetsDeduplicatesSpellings(t *testing.T) {
	fields := rules.FieldSet{
		ToolName: "apply_patch",
		ToolInputCommand: "*** Begin Patch\n" +
			"*** Update File: /repo/a.go\n" +
			"*** Update File: /repo/sub/../a.go\n" +
			"*** End Patch\n",
		CWD: "/repo",
	}
	if got := fields.PatchWriteTargets(); got != "/repo/a.go" {
		t.Fatalf("PatchWriteTargets() = %q, want one entry for /repo/a.go", got)
	}
}
