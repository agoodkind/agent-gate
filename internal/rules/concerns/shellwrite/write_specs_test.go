package shellwrite_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
	shellwriteconcern "goodkind.io/agent-gate/internal/rules/concerns/shellwrite"
)

func TestExtractWriteTargetsWithSpecs(t *testing.T) {
	tests := []struct {
		name    string
		command string
		spec    config.ShellWriteSpec
		want    []string
	}{
		{
			name:    "rm all operands",
			command: "rm -f first.txt second.txt",
			spec:    writeSpec([]string{"rm"}, config.WriteTargetAllOperands),
			want:    []string{"/repo/first.txt", "/repo/second.txt"},
		},
		{
			name:    "mkdir all operands",
			command: "mkdir first second",
			spec:    writeSpec([]string{"mkdir"}, config.WriteTargetAllOperands),
			want:    []string{"/repo/first", "/repo/second"},
		},
		{
			name:    "touch all operands",
			command: "touch first.txt second.txt",
			spec:    writeSpec([]string{"touch"}, config.WriteTargetAllOperands),
			want:    []string{"/repo/first.txt", "/repo/second.txt"},
		},
		{
			name:    "cp last operand",
			command: "cp source.txt destination.txt",
			spec:    writeSpec([]string{"cp"}, config.WriteTargetLastOperand),
			want:    []string{"/repo/destination.txt"},
		},
		{
			name:    "mv last operand",
			command: "mv source.txt destination.txt",
			spec:    writeSpec([]string{"mv"}, config.WriteTargetLastOperand),
			want:    []string{"/repo/destination.txt"},
		},
		{
			name:    "editor-style last operand",
			command: "editor session.txt destination.txt",
			spec:    writeSpec([]string{"editor"}, config.WriteTargetLastOperand),
			want:    []string{"/repo/destination.txt"},
		},
		{
			name:    "flag value is skipped",
			command: "writer-all --reference template.txt output.txt",
			spec: config.ShellWriteSpec{
				Argv0:               []string{"writer-all"},
				TargetMode:          config.WriteTargetAllOperands,
				SkipFlagsWithValues: []string{"--reference"},
			},
			want: []string{"/repo/output.txt"},
		},
		{
			name:    "attached flag value is skipped",
			command: "writer-all --reference=template.txt output.txt",
			spec: config.ShellWriteSpec{
				Argv0:               []string{"writer-all"},
				TargetMode:          config.WriteTargetAllOperands,
				SkipFlagsWithValues: []string{"--reference"},
			},
			want: []string{"/repo/output.txt"},
		},
		{
			name:    "quoted flag and value are skipped",
			command: `writer-all "--reference" /tmp/ref output.txt`,
			spec: config.ShellWriteSpec{
				Argv0:               []string{"writer-all"},
				TargetMode:          config.WriteTargetAllOperands,
				SkipFlagsWithValues: []string{"--reference"},
			},
			want: []string{"/repo/output.txt"},
		},
		{
			name:    "double hyphen ends option parsing",
			command: "writer-all -- -leading.txt regular.txt",
			spec: config.ShellWriteSpec{
				Argv0:        []string{"writer-all"},
				TargetMode:   config.WriteTargetAllOperands,
				EndOfOptions: true,
			},
			want: []string{"/repo/-leading.txt", "/repo/regular.txt"},
		},
		{
			name:    "double hyphen does not end option parsing when disabled",
			command: "writer-all -- -leading.txt regular.txt",
			spec: config.ShellWriteSpec{
				Argv0:      []string{"writer-all"},
				TargetMode: config.WriteTargetAllOperands,
			},
			want: []string{"/repo/regular.txt"},
		},
		{
			name:    "quoted double hyphen ends option parsing",
			command: `writer-all "--" -leading.txt regular.txt`,
			spec: config.ShellWriteSpec{
				Argv0:        []string{"writer-all"},
				TargetMode:   config.WriteTargetAllOperands,
				EndOfOptions: true,
			},
			want: []string{"/repo/-leading.txt", "/repo/regular.txt"},
		},
		{
			name:    "cwd flag rebases operands",
			command: "writer-all --cwd /other note.txt",
			spec: config.ShellWriteSpec{
				Argv0:      []string{"writer-all"},
				TargetMode: config.WriteTargetAllOperands,
				CwdFlags:   []string{"--cwd"},
			},
			want: []string{"/other/note.txt"},
		},
		{
			name:    "attached cwd flag rebases operands",
			command: "writer-all --cwd=/other note.txt",
			spec: config.ShellWriteSpec{
				Argv0:      []string{"writer-all"},
				TargetMode: config.WriteTargetAllOperands,
				CwdFlags:   []string{"--cwd"},
			},
			want: []string{"/other/note.txt"},
		},
		{
			name:    "quoted cwd flag rebases operands",
			command: `writer-all "--cwd" /other note.txt`,
			spec: config.ShellWriteSpec{
				Argv0:      []string{"writer-all"},
				TargetMode: config.WriteTargetAllOperands,
				CwdFlags:   []string{"--cwd"},
			},
			want: []string{"/other/note.txt"},
		},
		{
			name:    "literal assignment expands",
			command: `R=/repo/main; writer-all "$R/file.txt"`,
			spec:    writeSpec([]string{"writer-all"}, config.WriteTargetAllOperands),
			want:    []string{"/repo/main/file.txt"},
		},
		{
			name:    "absolute argv0 matches basename",
			command: "/bin/touch created.txt",
			spec:    writeSpec([]string{"touch"}, config.WriteTargetAllOperands),
			want:    []string{"/repo/created.txt"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(test.command, "/repo", []config.ShellWriteSpec{test.spec})
			if len(targets) != len(test.want) {
				t.Fatalf("targets = %#v, want paths %v", targets, test.want)
			}
			for i, want := range test.want {
				if targets[i].Path != want || targets[i].Reason != shellwriteconcern.ReasonOK {
					t.Fatalf("target %d = %#v, want path %q", i, targets[i], want)
				}
			}
		})
	}
}

func TestExtractWriteTargetsWithSpecsDeduplicatesRedirectTarget(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		"tee output.txt",
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{"tee"}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 1 || targets[0].Path != "/repo/output.txt" {
		t.Fatalf("targets = %#v, want one deduplicated output target", targets)
	}
}

func TestExtractWriteTargetsWithSpecsWriterShapes(t *testing.T) {
	removeName := "r" + "m"
	makeDirectoryName := "mk" + "dir"
	updateTimeName := "to" + "uch"
	copyName := "c" + "p"
	moveName := "m" + "v"
	specs := []config.ShellWriteSpec{
		writeSpec([]string{removeName, makeDirectoryName, updateTimeName}, config.WriteTargetAllOperands),
		writeSpec([]string{copyName, moveName, "nvim"}, config.WriteTargetLastOperand),
	}
	tests := []struct {
		command string
		want    []string
	}{
		{command: removeName + " first.txt second.txt", want: []string{"/repo/first.txt", "/repo/second.txt"}},
		{command: makeDirectoryName + " first second", want: []string{"/repo/first", "/repo/second"}},
		{command: updateTimeName + " first.txt second.txt", want: []string{"/repo/first.txt", "/repo/second.txt"}},
		{command: copyName + " source.txt destination.txt", want: []string{"/repo/destination.txt"}},
		{command: moveName + " source.txt destination.txt", want: []string{"/repo/destination.txt"}},
		{command: "nvim first.txt second.txt", want: []string{"/repo/second.txt"}},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(test.command, "/repo", specs)
			if len(targets) != len(test.want) {
				t.Fatalf("targets = %#v, want %v", targets, test.want)
			}
			for i, want := range test.want {
				if targets[i].Path != want {
					t.Fatalf("target %d = %#v, want %q", i, targets[i], want)
				}
			}
		})
	}
}

func TestExtractWriteTargetsWithSpecsMatchesAbsoluteArgv0ByBasename(t *testing.T) {
	commandName := "to" + "uch"
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		"/bin/"+commandName+" generated.txt",
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{commandName}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 1 || targets[0].Path != "/repo/generated.txt" {
		t.Fatalf("targets = %#v, want /repo/generated.txt", targets)
	}
}

func TestExtractWriteTargetsWithSpecsPreservesDistinctRedirectTarget(t *testing.T) {
	commandName := "to" + "uch"
	command := commandName + " generated.txt " + ">" + " activity.log"
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		command,
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{commandName}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 2 || targets[0].Path != "/repo/activity.log" || targets[1].Path != "/repo/generated.txt" {
		t.Fatalf("targets = %#v, want distinct redirect and declared targets", targets)
	}
}

func TestExtractWriteTargetsWithSpecsPreservesFileDescriptorRedirect(t *testing.T) {
	commandName := "to" + "uch"
	command := commandName + " generated.txt 2" + ">" + "&1"
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		command,
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{commandName}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 1 || targets[0].Path != "/repo/generated.txt" {
		t.Fatalf("targets = %#v, want only /repo/generated.txt", targets)
	}
}

func TestExtractWriteTargetsWithSpecsUnresolvedOperand(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		`writer-all "$TARGET"`,
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{"writer-all"}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 1 || targets[0].Reason != shellwriteconcern.ReasonUnparsedCommandShape {
		t.Fatalf("targets = %#v, want one unparsed sentinel", targets)
	}
}

func TestExtractWriteTargetsWithSpecsUnresolvedControlToken(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		`writer-all "$FLAG" note.txt`,
		"/repo",
		[]config.ShellWriteSpec{{
			Argv0:               []string{"writer-all"},
			TargetMode:          config.WriteTargetAllOperands,
			SkipFlagsWithValues: []string{"--reference"},
		}},
	)
	if len(targets) != 2 {
		t.Fatalf("targets = %#v, want unresolved sentinel and note.txt", targets)
	}
	if targets[0].Reason != shellwriteconcern.ReasonUnparsedCommandShape || targets[0].Path != "" {
		t.Fatalf("unresolved target = %#v, want unparsed sentinel", targets[0])
	}
	if targets[1].Reason != shellwriteconcern.ReasonOK || targets[1].Path != "/repo/note.txt" {
		t.Fatalf("resolved target = %#v, want /repo/note.txt", targets[1])
	}
}

func TestExtractWriteTargetsWithSpecsIgnoresNonmatchingCommand(t *testing.T) {
	targets := shellwriteconcern.ExtractWriteTargetsWithSpecs(
		"inspect file.txt",
		"/repo",
		[]config.ShellWriteSpec{writeSpec([]string{"writer-all"}, config.WriteTargetAllOperands)},
	)
	if len(targets) != 0 {
		t.Fatalf("targets = %#v, want none", targets)
	}
}

func writeSpec(argv0 []string, targetMode string) config.ShellWriteSpec {
	return config.ShellWriteSpec{Argv0: argv0, TargetMode: targetMode}
}
