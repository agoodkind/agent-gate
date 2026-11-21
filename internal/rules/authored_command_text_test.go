package rules_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/rules"
)

func TestAuthoredCommandText(t *testing.T) {
	testCases := []struct {
		name    string
		command string
		want    string
	}{
		{
			name: "update hunk",
			command: "*** Begin Patch\n*** Update File: /r/a.go\n@@ func A\n" +
				"ctx\n-old prose\n+new prose\n*** End Patch\n",
			want: "new prose\n",
		},
		{
			name: "text around envelope",
			command: "echo before\n*** Begin Patch\n*** Add File: /r/b.go\n" +
				"+package b\n*** End Patch\necho after",
			want: "echo before\npackage b\necho after",
		},
		{
			name:    "shell command",
			command: "rm -rf /repo/internal",
			want:    "rm -rf /repo/internal",
		},
	}

	for _, testCase := range testCases {
		t.Run(
			testCase.name,
			func(t *testing.T) {
				fields := rules.FieldSet{ToolInputCommand: testCase.command}
				if got := fields.AuthoredCommandText(); got != testCase.want {
					t.Fatalf("AuthoredCommandText() = %q, want %q", got, testCase.want)
				}
			},
		)
	}
}
