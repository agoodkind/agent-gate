package rules

import (
	"strings"
	"testing"
)

// TestBuildJudgeInputBashCall confirms a shell call surfaces the chat working
// directory, the verbatim command, the structural AST parse (argv0 and read
// target), and the recent conversation tail, so the judge sees both the
// situation and the thing being judged.
func TestBuildJudgeInputBashCall(t *testing.T) {
	fields := FieldSet{
		ToolName:         "Bash",
		ToolInputCommand: "cat go.mod",
		CWD:              "/repo",
	}
	tail := "user: please read go.mod\nassistant: on it"

	out := buildJudgeInput(fields, tail, nil)

	for _, want := range []string{
		"/repo",              // chat working directory
		"cat go.mod",         // verbatim command
		"reads:",             // AST structure section
		"go.mod",             // AST read target
		"please read go.mod", // conversation tail
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

// TestBuildJudgeInputEffectiveCwdDiffers confirms a command that cd's away from
// the session directory surfaces both the chat working directory and the
// effective tool-call directory, because running in a different directory than
// the project is decision-relevant.
func TestBuildJudgeInputEffectiveCwdDiffers(t *testing.T) {
	fields := FieldSet{
		ToolName:         "Bash",
		ToolInputCommand: "cd sub && cat x",
		CWD:              "/repo",
	}

	out := buildJudgeInput(fields, "", nil)

	if !strings.Contains(out, "/repo") {
		t.Fatalf("output missing session cwd /repo:\n%s", out)
	}
	if !strings.Contains(out, "/repo/sub") {
		t.Fatalf("output missing effective cwd /repo/sub:\n%s", out)
	}
}

// TestBuildJudgeInputWriteCall confirms a write tool surfaces the target file
// path as a write target without shell-parsing it, and shows only a bounded
// snippet of the new content rather than dumping the whole body.
func TestBuildJudgeInputWriteCall(t *testing.T) {
	longContent := strings.Repeat("A", 500)
	fields := FieldSet{
		ToolName:          "Write",
		ToolInputFilePath: "/repo/foo.go",
		ToolInputContent:  longContent,
		CWD:               "/repo",
	}

	out := buildJudgeInput(fields, "", nil)

	if !strings.Contains(out, "/repo/foo.go") {
		t.Fatalf("output missing write target path:\n%s", out)
	}
	if !strings.Contains(out, "writes:") {
		t.Fatalf("output missing structural write target:\n%s", out)
	}
	if strings.Contains(out, "cmd:") {
		t.Fatalf("write call should not be shell-parsed (found cmd: section):\n%s", out)
	}
	if strings.Contains(out, unparseableCommandMarker) {
		t.Fatalf("write call should not attempt a shell parse:\n%s", out)
	}
	if strings.Contains(out, strings.Repeat("A", 200)) {
		t.Fatalf("write content was not bounded to a snippet:\n%s", out)
	}
}

// TestBuildJudgeInputEmptyTranscript confirms the conversation panel is dropped
// when the transcript tail is empty, while the tool call and working directory
// still render.
func TestBuildJudgeInputEmptyTranscript(t *testing.T) {
	fields := FieldSet{
		ToolName:         "Bash",
		ToolInputCommand: "ls",
		CWD:              "/repo",
	}

	out := buildJudgeInput(fields, "", nil)

	if strings.Contains(out, "recent conversation") {
		t.Fatalf("empty transcript should drop the conversation panel:\n%s", out)
	}
	if !strings.Contains(out, "ls") {
		t.Fatalf("output missing verbatim command ls:\n%s", out)
	}
	if !strings.Contains(out, "/repo") {
		t.Fatalf("output missing chat working directory:\n%s", out)
	}
}
