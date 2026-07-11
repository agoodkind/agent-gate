package oracle

import "testing"

func TestSearchGoldCases(t *testing.T) {
	const agentGateRoot = "/Users/agoodkind/Sites/agent-gate"
	const lmdRoot = "/Users/agoodkind/Sites/lmd"

	roots := []string{agentGateRoot, lmdRoot}
	cases := []struct {
		id      string
		command string
		cwd     string
		want    Verdict
	}{
		{
			id:      "g01",
			command: `grep -rn "func " .`,
			cwd:     agentGateRoot,
			want:    Block,
		},
		{
			id:      "g02",
			command: `F=` + agentGateRoot + `; grep -n module "$F/go.mod"`,
			cwd:     "/tmp",
			want:    Block,
		},
		{
			id:      "g03",
			command: `echo ` + agentGateRoot + `/go.mod | xargs grep module`,
			cwd:     "/tmp",
			want:    Block,
		},
		{
			id:      "g04",
			command: `sh -c 'D=` + agentGateRoot + `; grep -rn foo "$D"'`,
			cwd:     "/tmp",
			want:    Unknown,
		},
		{
			id:      "g05",
			command: `find ` + agentGateRoot + ` -name '*.go' -exec grep -l foo {} +`,
			cwd:     "/tmp",
			want:    Block,
		},
		{
			id:      "g06",
			command: `eval "$(printf 'grep -rn foo ` + agentGateRoot + `')"`,
			cwd:     "/tmp",
			want:    Unknown,
		},
		{
			id:      "g07",
			command: "rg --json TODO",
			cwd:     lmdRoot,
			want:    Block,
		},
		{
			id:      "g08",
			command: "git grep -n TODO",
			cwd:     agentGateRoot,
			want:    Block,
		},
		{
			id:      "g09",
			command: "grep -rn secret internal/",
			cwd:     agentGateRoot,
			want:    Block,
		},
		{
			id:      "g10",
			command: "ps aux | grep lmd",
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
		{
			id:      "g11",
			command: "grep -n root /etc/hosts",
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
		{
			id:      "g12",
			command: "history | grep git",
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
		{
			id:      "g13",
			command: "grep -rn foo /var/log/system.log",
			cwd:     "/tmp",
			want:    Allow,
		},
		{
			id:      "g14",
			command: `echo "$PATH" | grep bin`,
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
		{
			id:      "g15",
			command: "grep --version",
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
		{
			id:      "g16",
			command: `ls | grep '\.go'`,
			cwd:     "/tmp",
			want:    Allow,
		},
		{
			id:      "g17",
			command: "echo hello | grep hello",
			cwd:     agentGateRoot,
			want:    Allow,
		},
		{
			id:      "g18",
			command: "grep -rn foo /opt/homebrew/etc",
			cwd:     "/tmp",
			want:    Allow,
		},
		{
			id:      "g19",
			command: "cat /var/log/install.log | grep -i error",
			cwd:     "/Users/agoodkind",
			want:    Allow,
		},
	}

	unknownCount := 0
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := Search(tc.command, tc.cwd, roots)
			if got != tc.want {
				t.Fatalf("Search(%q, %q) = %v, want %v", tc.command, tc.cwd, got, tc.want)
			}
			if got == Unknown {
				unknownCount++
			}
		})
	}
	if unknownCount != 2 {
		t.Fatalf("unknown count = %d, want 2", unknownCount)
	}
}

func TestSearchLiteralAssignmentExpansionBlocksIndexedRoot(t *testing.T) {
	const agentGateRoot = "/Users/agoodkind/Sites/agent-gate"

	cases := []struct {
		id      string
		command string
		cwd     string
		want    Verdict
	}{
		{
			id:      "literal assignment indexed root",
			command: `D=` + agentGateRoot + `; gr` + `ep -rn foo "$D"`,
			cwd:     "/tmp",
			want:    Block,
		},
	}

	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			got := Search(tc.command, tc.cwd, []string{agentGateRoot})
			if got != tc.want {
				t.Fatalf("Search(%q, %q) = %v, want %v", tc.command, tc.cwd, got, tc.want)
			}
		})
	}
}
