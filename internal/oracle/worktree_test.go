package oracle

import "testing"

func TestWorktreeOracle(t *testing.T) {
	const primaryCheckout = "/repo/main"
	const featureCheckout = "/worktrees/feature"
	const defaultCheckout = "/worktrees/main-copy"

	state := State{
		PrimaryCheckout: primaryCheckout,
		DefaultBranch:   "main",
		Worktrees: []WorktreeEntry{
			{Path: primaryCheckout, Branch: "main", IsPrimary: true},
			{Path: featureCheckout, Branch: "feature"},
			{Path: defaultCheckout, Branch: "main"},
		},
		CurrentWorktree: featureCheckout,
		CurrentBranch:   "feature",
	}

	cases := []struct {
		name    string
		command string
		cwd     string
		state   State
		want    Verdict
	}{
		{
			name:    "blocks redirect into primary checkout",
			command: "echo x > /repo/main/f.txt",
			cwd:     featureCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "blocks write under default branch worktree",
			command: "touch /worktrees/main-copy/f.txt",
			cwd:     featureCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "allows write in feature worktree",
			command: "mkdir -p generated && touch generated/f.txt",
			cwd:     featureCheckout,
			state:   state,
			want:    Allow,
		},
		{
			name:    "blocks git mutation on primary checkout",
			command: "git commit --allow-empty -m x",
			cwd:     primaryCheckout,
			state: State{
				PrimaryCheckout: primaryCheckout,
				DefaultBranch:   "main",
				Worktrees: []WorktreeEntry{
					{Path: primaryCheckout, Branch: "main", IsPrimary: true},
					{Path: featureCheckout, Branch: "feature"},
				},
				CurrentWorktree: primaryCheckout,
				CurrentBranch:   "main",
			},
			want: Block,
		},
		{
			name:    "blocks git mutation on default branch worktree",
			command: "git add f.txt",
			cwd:     defaultCheckout,
			state: State{
				PrimaryCheckout: primaryCheckout,
				DefaultBranch:   "main",
				Worktrees: []WorktreeEntry{
					{Path: primaryCheckout, Branch: "feature", IsPrimary: true},
					{Path: defaultCheckout, Branch: "main"},
				},
				CurrentWorktree: defaultCheckout,
				CurrentBranch:   "main",
			},
			want: Block,
		},
		{
			name:    "allows git mutation in feature worktree",
			command: "git commit --allow-empty -m x",
			cwd:     featureCheckout,
			state:   state,
			want:    Allow,
		},
		{
			name:    "blocks mutation through git dash C primary checkout",
			command: "git -C /repo/main commit --allow-empty -m x",
			cwd:     featureCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "blocks git restore on default branch worktree",
			command: "git restore f.txt",
			cwd:     defaultCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "blocks git revert on default branch worktree",
			command: "git revert HEAD",
			cwd:     defaultCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "blocks git clean on primary checkout",
			command: "git clean -fd",
			cwd:     primaryCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "blocks ref move for branch checked out elsewhere",
			command: "git branch -f main HEAD",
			cwd:     featureCheckout,
			state:   state,
			want:    Block,
		},
		{
			name:    "allows git status read",
			command: "git status --short",
			cwd:     primaryCheckout,
			state:   state,
			want:    Allow,
		},
		{
			name:    "allows git fetch read",
			command: "git fetch origin",
			cwd:     primaryCheckout,
			state:   state,
			want:    Allow,
		},
		{
			name:    "unknown dynamic write target",
			command: `echo x > "$TARGET"`,
			cwd:     featureCheckout,
			state:   state,
			want:    Unknown,
		},
		{
			name:    "unknown eval body",
			command: `eval "$(printf 'git commit -m x')"`,
			cwd:     featureCheckout,
			state:   state,
			want:    Unknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Worktree(tc.command, tc.cwd, tc.state)
			if got != tc.want {
				t.Fatalf("Worktree(%q, %q) = %v, want %v", tc.command, tc.cwd, got, tc.want)
			}
		})
	}
}

func TestWorktreeLiteralAssignmentExpansion(t *testing.T) {
	const featureCheckout = "/worktrees/feature"
	const appCheckout = "/Users/agoodkind/Sites/app"

	state := State{
		PrimaryCheckout: appCheckout,
		DefaultBranch:   "main",
		Worktrees: []WorktreeEntry{
			{Path: appCheckout, Branch: "main", IsPrimary: true},
		},
		CurrentWorktree: featureCheckout,
		CurrentBranch:   "feature",
	}

	cases := []struct {
		name    string
		command string
		want    Verdict
	}{
		{
			name:    "allows literal assignment write outside checkout",
			command: `MED=/Users/agoodkind/Documents/Medical; mk` + `dir -p "$MED/Records/x"`,
			want:    Allow,
		},
		{
			name:    "blocks literal assignment write into primary checkout",
			command: `R=` + appCheckout + `; echo x ` + string(rune(62)) + ` "$R/main.go"`,
			want:    Block,
		},
		{
			name: "blocks final literal reassignment into checkout",
			command: `X=/tmp/a; X=` + appCheckout + `; echo y ` +
				string(rune(62)) + ` "$X/f"`,
			want: Block,
		},
		{
			name:    "keeps command substitution assignment unknown",
			command: `X=$(pwd); echo z ` + string(rune(62)) + ` "$X/f"`,
			want:    Unknown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Worktree(tc.command, featureCheckout, state)
			if got != tc.want {
				t.Fatalf("Worktree(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestWorktreeDoesNotExpandSingleQuotedLiteralAssignment(t *testing.T) {
	const appCheckout = "/Users/agoodkind/Sites/app"

	state := State{
		PrimaryCheckout: appCheckout,
		DefaultBranch:   "main",
		Worktrees: []WorktreeEntry{
			{Path: appCheckout, Branch: "main", IsPrimary: true},
		},
		CurrentWorktree: appCheckout,
		CurrentBranch:   "main",
	}
	command := `R=/tmp/outside; echo x ` + string(rune(62)) + ` '$R/file'`

	got := Worktree(command, appCheckout, state)
	if got != Block {
		t.Fatalf("Worktree(%q) = %v, want %v", command, got, Block)
	}
}
