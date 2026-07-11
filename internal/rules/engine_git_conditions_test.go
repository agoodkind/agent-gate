package rules

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/gitbranch"
	"goodkind.io/gksyntax/shelldecomp"
)

type gitStateFixture struct {
	state gitbranch.State
	err   error
	paths []string
}

func (fixture *gitStateFixture) read(path string) (gitbranch.State, error) {
	fixture.paths = append(fixture.paths, path)
	return fixture.state, fixture.err
}

func TestGitPrimaryCheckoutConditionMatch(t *testing.T) {
	state := testGitState()
	cases := []struct {
		name      string
		fields    FieldSet
		condition string
		context   conditionContext
		want      bool
	}{
		{name: "primary path", fields: FieldSet{ToolInputFilePath: "/repo/main/file.go", CWD: "/repo/feature"}, condition: `field_paths = ["tool_input.file_path"]`, want: true},
		{name: "nested nonexistent primary path", fields: FieldSet{ToolInputFilePath: "/repo/main/new/nested/file.go", CWD: "/repo/feature"}, condition: `field_paths = ["tool_input.file_path"]`, want: true},
		{name: "feature worktree path", fields: FieldSet{ToolInputFilePath: "/repo/feature/file.go", CWD: "/repo/feature"}, condition: `field_paths = ["tool_input.file_path"]`, want: false},
		{name: "relative field path", fields: FieldSet{ToolInputFilePath: "file.go", CWD: "/repo/main"}, condition: `field_paths = ["tool_input.file_path"]`, want: true},
		{name: "preceding command cwd", fields: FieldSet{CWD: "/tmp"}, context: conditionContext{commandCwds: []string{"/repo/main"}}, want: true},
		{
			name:   "declared write spec target",
			fields: FieldSet{ToolInputCommand: "writer file.go", CWD: "/repo/main"},
			condition: `field_paths = ["cmd_write_targets"]
[[rules.conditions.write_specs]]
argv0 = ["writer"]
target_mode = "all_operands"`,
			want: true,
		},
		{name: "unresolved target", fields: FieldSet{ToolInputFilePath: string([]byte{'x', 0, 'y'}), CWD: "/repo/main"}, condition: `field_paths = ["tool_input.file_path"]`, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			condition := loadGitCondition(t, "git_primary_checkout", tc.condition)
			fixture := &gitStateFixture{state: state}
			got := gitPrimaryCheckoutConditionMatch(tc.fields, condition, tc.context, fixture.read)
			if got != tc.want {
				t.Fatalf("gitPrimaryCheckoutConditionMatch() = %v, want %v; paths = %v", got, tc.want, fixture.paths)
			}
		})
	}
}

func TestGitPrimaryCheckoutConditionStateFailureFailsOpen(t *testing.T) {
	condition := loadGitCondition(t, "git_primary_checkout", `field_paths = ["tool_input.file_path"]`)
	fixture := &gitStateFixture{err: errors.New("state unavailable")}
	fields := FieldSet{ToolInputFilePath: "/repo/main/file.go", CWD: "/repo/main"}
	if gitPrimaryCheckoutConditionMatch(fields, condition, conditionContext{}, fixture.read) {
		t.Fatal("state-reader failure matched")
	}
}

func TestGitConditionsDispatchWithInjectedState(t *testing.T) {
	fixture := &gitStateFixture{state: testGitState()}
	primaryRule := loadGitRule(t, `
[[rules.conditions]]
kind = "command"
argv0 = "git"
subcommands = ["status"]
cwd_flags = ["-C"]

[[rules.conditions]]
kind = "git_primary_checkout"
`)
	primaryFields := FieldSet{
		ToolInputCommand: "git -C /repo/main status --short",
		CWD:              "/repo/feature",
	}
	cwds, matched := commandConditionCwds(primaryFields, &primaryRule.Conditions[0])
	if !matched {
		t.Fatal("command condition did not resolve the redirected cwd")
	}
	condCtx := conditionContext{commandCwds: cwds}
	if !gitConditionMatch(
		primaryFields,
		&primaryRule.Conditions[1],
		condCtx,
		fixture.read,
	) {
		t.Fatal("primary checkout condition did not dispatch")
	}

	refRule := loadGitRule(t, `
[[rules.conditions]]
kind = "git_ref_move"
`)
	refFields := FieldSet{
		ToolInputCommand: "git update-ref refs/heads/main HEAD",
		CWD:              "/repo/feature",
	}
	if !gitConditionMatch(
		refFields,
		&refRule.Conditions[0],
		conditionContext{},
		fixture.read,
	) {
		t.Fatal("ref move condition did not dispatch")
	}
}

func TestGitDefaultBranchConditionDispatchesWithInjectedState(t *testing.T) {
	fixture := &gitStateFixture{state: testGitState()}
	condition := loadGitCondition(
		t, "git_default_branch", `field_paths = ["tool_input.file_path"]`,
	)
	fields := FieldSet{
		ToolInputFilePath: "/repo/main/file.go",
		CWD:               "/repo/feature",
	}

	if !gitConditionMatch(
		fields, condition, conditionContext{}, fixture.read,
	) {
		t.Fatal("default branch condition did not use the injected state")
	}
}

func TestEvaluateAllUsesContextGitStateReader(t *testing.T) {
	fixture := &gitStateFixture{state: testGitState()}
	rule := loadGitRule(t, `
[[rules.conditions]]
kind = "git_default_branch"
field_paths = ["tool_input.file_path"]
`)
	fields := FieldSet{
		ToolInputFilePath: "/repo/main/file.go",
		CWD:               "/repo/feature",
	}
	ctx := WithGitStateReader(context.Background(), fixture.read)

	violations := EvaluateAll(ctx, "codex", "AnyEvent", fields, []config.Rule{*rule}, nil)

	if len(violations) != 1 {
		t.Fatalf("violations = %+v, want one", violations)
	}
}

func TestGitRefMoveConditionMatch(t *testing.T) {
	cases := []struct {
		name    string
		command string
		cwd     string
		want    bool
	}{
		{name: "branch force", command: "git branch -f main HEAD", cwd: "/repo/feature", want: true},
		{name: "branch force copy", command: "git branch -F main feature", cwd: "/repo/feature", want: true},
		{name: "branch long force", command: "git branch --force main HEAD", cwd: "/repo/feature", want: true},
		{name: "branch delete force", command: "git branch -D main", cwd: "/repo/feature", want: true},
		{name: "branch delete", command: "git branch --delete main", cwd: "/repo/feature", want: true},
		{name: "branch rename checked out source", command: "git branch -m main renamed", cwd: "/repo/feature", want: true},
		{name: "branch force rename checked out source", command: "git branch -M release/v1 renamed", cwd: "/repo/feature", want: true},
		{name: "branch long rename checked out source", command: "git branch --move main renamed", cwd: "/repo/feature", want: true},
		{name: "branch rename unchecked source", command: "git branch -m old main", cwd: "/repo/feature", want: false},
		{name: "branch current rename", command: "git branch -m main", cwd: "/repo/feature", want: false},
		{name: "combined branch flags", command: "git branch -df main", cwd: "/repo/feature", want: true},
		{name: "combined branch flags reversed", command: "git branch -fd main", cwd: "/repo/feature", want: true},
		{name: "force rename cluster", command: "git branch -fm main renamed", cwd: "/repo/feature", want: true},
		{name: "rename force cluster", command: "git branch -mf main renamed", cwd: "/repo/feature", want: true},
		{name: "force uppercase rename cluster", command: "git branch -fM main renamed", cwd: "/repo/feature", want: true},
		{name: "uppercase rename force cluster", command: "git branch -Mf main renamed", cwd: "/repo/feature", want: true},
		{name: "force copy cluster", command: "git branch -fc main copied", cwd: "/repo/feature", want: false},
		{name: "copy force cluster", command: "git branch -cf main copied", cwd: "/repo/feature", want: false},
		{name: "force uppercase copy cluster", command: "git branch -fC main copied", cwd: "/repo/feature", want: false},
		{name: "uppercase copy force cluster", command: "git branch -Cf main copied", cwd: "/repo/feature", want: false},
		{name: "unknown branch short option", command: "git branch -not-a-real-flag main", cwd: "/repo/feature", want: false},
		{name: "unknown branch cluster character", command: "git branch -fZ main HEAD", cwd: "/repo/feature", want: false},
		{name: "update ref", command: "git update-ref refs/heads/main HEAD", cwd: "/repo/feature", want: true},
		{name: "update ref with old value", command: "git update-ref refs/heads/main HEAD OLD", cwd: "/repo/feature", want: true},
		{name: "update ref missing new value", command: "git update-ref refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "update ref too many values", command: "git update-ref refs/heads/main HEAD OLD EXTRA", cwd: "/repo/feature", want: false},
		{name: "delete update ref", command: "git update-ref -d refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "delete update ref with old value", command: "git update-ref -d refs/heads/main OLD", cwd: "/repo/feature", want: true},
		{name: "delete update ref missing ref", command: "git update-ref -d", cwd: "/repo/feature", want: false},
		{name: "invalid update ref delete flag", command: "git update-ref -D refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "checkout reset", command: "git checkout -B main HEAD", cwd: "/repo/feature", want: true},
		{name: "switch reset", command: "git switch -C main", cwd: "/repo/feature", want: true},
		{name: "local push", command: "git push /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local forced push", command: "git push /repo/main +HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local delete push", command: "git push /repo/main :refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local delete option push", command: "git push --delete /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local short delete push", command: "git push -d /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local force delete cluster", command: "git push -fd /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local delete force cluster", command: "git push -df /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local short refspec push", command: "git push /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local heads refspec push", command: "git push /repo/main HEAD:heads/main", cwd: "/repo/feature", want: true},
		{name: "local file URL push", command: "git push file:///repo/main HEAD:main", cwd: "/repo/feature", want: true},
		{name: "local relative push", command: "git push ../main HEAD:main", cwd: "/repo/feature", want: true},
		{name: "local repo option push", command: "git push --repo /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local inline repo option push", command: "git push --repo=/repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local repo option delete", command: "git push --delete --repo /repo/main main", cwd: "/repo/feature", want: true},
		{name: "local push all branches", command: "git push --all /repo/main", cwd: "/repo/feature", want: true},
		{name: "local push mirror", command: "git push --mirror /repo/main", cwd: "/repo/feature", want: true},
		{name: "mirror survives no all", command: "git push --mirror --no-all /repo/main", cwd: "/repo/feature", want: true},
		{name: "all survives no mirror", command: "git push --all --no-mirror /repo/main", cwd: "/repo/feature", want: true},
		{name: "negated all without refspec", command: "git push --all --no-all /repo/main", cwd: "/repo/feature", want: false},
		{name: "negated mirror without refspec", command: "git push --mirror --no-mirror /repo/main", cwd: "/repo/feature", want: false},
		{name: "all dry run", command: "git push --all --dry-run /repo/main", cwd: "/repo/feature", want: false},
		{name: "all negated dry run", command: "git push --all --dry-run --no-dry-run /repo/main", cwd: "/repo/feature", want: true},
		{name: "all with explicit refspec", command: "git push --all /repo/main HEAD:main", cwd: "/repo/feature", want: false},
		{name: "mirror with explicit refspec", command: "git push --mirror /repo/main HEAD:main", cwd: "/repo/feature", want: false},
		{name: "all with mirror", command: "git push --all --mirror /repo/main", cwd: "/repo/feature", want: false},
		{name: "mirror with all", command: "git push --mirror --all /repo/main", cwd: "/repo/feature", want: false},
		{name: "delete with all", command: "git push --delete --all /repo/main", cwd: "/repo/feature", want: false},
		{name: "delete with branches", command: "git push --delete --branches /repo/main", cwd: "/repo/feature", want: false},
		{name: "delete with mirror", command: "git push --delete --mirror /repo/main", cwd: "/repo/feature", want: false},
		{name: "tags only", command: "git push --tags /repo/main", cwd: "/repo/feature", want: false},
		{name: "tags with all", command: "git push --tags --all /repo/main", cwd: "/repo/feature", want: false},
		{name: "tags with branches", command: "git push --tags --branches /repo/main", cwd: "/repo/feature", want: false},
		{name: "tags with mirror", command: "git push --tags --mirror /repo/main", cwd: "/repo/feature", want: false},
		{name: "all with tags", command: "git push --all --tags /repo/main", cwd: "/repo/feature", want: false},
		{name: "mirror with tags", command: "git push --mirror --tags /repo/main", cwd: "/repo/feature", want: false},
		{name: "tags with explicit refspec", command: "git push --tags /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "delete with tags", command: "git push --delete --tags /repo/main main", cwd: "/repo/feature", want: false},
		{name: "tags with delete", command: "git push --tags --delete /repo/main main", cwd: "/repo/feature", want: false},
		{name: "local force option push", command: "git push --force /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "branches with explicit refspec", command: "git push --branches /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local progress option push", command: "git push --progress /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local verify option push", command: "git push --verify /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local IPv4 option push", command: "git push -4 /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local IPv6 option push", command: "git push -6 /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local force lease push", command: "git push --force-with-lease=main /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local receive pack push", command: "git push --receive-pack git-receive-pack /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local push option", command: "git push --push-option ci.skip /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local recurse submodules push", command: "git push --recurse-submodules check /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local inline recurse submodules push", command: "git push --recurse-submodules=check /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local dry run push", command: "git push --dry-run /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local short dry run push", command: "git push -n /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local force dry run cluster", command: "git push -fn /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local dry run force cluster", command: "git push -nf /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local unknown short cluster", command: "git push -fz /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "local negated force push", command: "git push --no-force /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "local negated dry run push", command: "git push --dry-run --no-dry-run /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "unknown negated push option", command: "git push --no-not-a-real-option /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "bare signed push", command: "git push --signed /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "separate signed value", command: "git push --signed yes /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "valid inline signed push", command: "git push --signed=if-asked /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: true},
		{name: "invalid separate signed push", command: "git push --signed sometimes /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "invalid inline signed push", command: "git push --signed=sometimes /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "invalid separate recurse submodules", command: "git push --recurse-submodules sometimes /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "invalid inline recurse submodules", command: "git push --recurse-submodules=sometimes /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "unknown push option", command: "git push --not-a-real-option /repo/main HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "remote repo option", command: "git push --repo origin HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "remote URL repo option", command: "git push --repo=https://example.com/repo.git HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "slash branch", command: "git branch -f release/v1 HEAD", cwd: "/repo/feature", want: true},
		{name: "literal assignment", command: `B=main; git branch -f "$B" HEAD`, cwd: "/repo/feature", want: true},
		{name: "command wrapper", command: "command git branch -f main HEAD", cwd: "/repo/feature", want: true},
		{name: "git global flags", command: "git -c user.name=x --no-pager branch -f main HEAD", cwd: "/repo/feature", want: true},
		{name: "git dash C", command: "git -C /repo/feature branch -f main HEAD", cwd: "/tmp", want: true},
		{name: "checkout option terminator", command: "git checkout -- -Bmain", cwd: "/repo/feature", want: false},
		{name: "switch option terminator", command: "git switch -- -Cmain", cwd: "/repo/feature", want: false},
		{name: "checkout trailing option terminator", command: "git checkout -B main -- file", cwd: "/repo/feature", want: false},
		{name: "switch trailing option terminator", command: "git switch -C main -- file", cwd: "/repo/feature", want: false},
		{name: "checkout excess start points", command: "git checkout -B main start extra", cwd: "/repo/feature", want: false},
		{name: "switch excess start points", command: "git switch -C main start extra", cwd: "/repo/feature", want: false},
		{name: "checkout unknown option", command: "git checkout --not-a-real-option -B main", cwd: "/repo/feature", want: false},
		{name: "switch unknown option", command: "git switch --not-a-real-option -C main", cwd: "/repo/feature", want: false},
		{name: "checkout conflicting detach", command: "git checkout --detach -B main", cwd: "/repo/feature", want: false},
		{name: "switch conflicting detach", command: "git switch --detach -C main", cwd: "/repo/feature", want: false},
		{name: "checkout quiet reset", command: "git checkout --quiet -B main", cwd: "/repo/feature", want: true},
		{name: "switch progress reset", command: "git switch --progress -C main", cwd: "/repo/feature", want: true},
		{name: "checkout switch-only discard changes", command: "git checkout --discard-changes -B main", cwd: "/repo/feature", want: false},
		{name: "switch discard changes reset", command: "git switch --discard-changes -C main", cwd: "/repo/feature", want: true},
		{name: "checkout force merge conflict", command: "git checkout --force --merge -B main", cwd: "/repo/feature", want: false},
		{name: "switch discard merge conflict", command: "git switch --discard-changes --merge -C main", cwd: "/repo/feature", want: false},
		{name: "current worktree branch", command: "git branch -f feature HEAD", cwd: "/repo/feature", want: false},
		{name: "detached entry", command: "git branch -f detached HEAD", cwd: "/repo/feature", want: false},
		{name: "normal creation", command: "git branch new-branch HEAD", cwd: "/repo/feature", want: false},
		{name: "normal checked out branch creation", command: "git branch main HEAD", cwd: "/repo/feature", want: false},
		{name: "remote push", command: "git push origin HEAD:refs/heads/main", cwd: "/repo/feature", want: false},
		{name: "remote tracking ref", command: "git update-ref refs/remotes/origin/main HEAD", cwd: "/repo/feature", want: false},
		{name: "tag ref", command: "git update-ref refs/tags/main HEAD", cwd: "/repo/feature", want: false},
		{name: "dynamic branch", command: `git branch -f "$BRANCH" HEAD`, cwd: "/repo/feature", want: false},
		{name: "malformed branch", command: "git branch -f", cwd: "/repo/feature", want: false},
		{name: "branch read", command: "git branch --show-current", cwd: "/repo/feature", want: false},
		{name: "status read", command: "git status --short", cwd: "/repo/feature", want: false},
		{name: "reset current branch", command: "git reset --hard HEAD", cwd: "/repo/feature", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &gitStateFixture{state: testGitState()}
			fields := FieldSet{ToolInputCommand: tc.command, CWD: tc.cwd}
			got := gitRefMoveConditionMatch(fields, fixture.read)
			if got != tc.want {
				t.Fatalf("gitRefMoveConditionMatch(%q) = %v, want %v; paths = %v", tc.command, got, tc.want, fixture.paths)
			}
		})
	}
}

func TestGitRefMoveRepositorySelectingGlobalFlags(t *testing.T) {
	cases := []struct {
		name     string
		command  string
		cwd      string
		wantPath string
		want     bool
	}{
		{name: "separate git directory", command: "git --git-dir /repo/main/.git branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/main", want: true},
		{name: "inline git directory", command: "git --git-dir=/repo/main/.git branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/main", want: true},
		{name: "separate work tree", command: "git --work-tree /unrelated branch -f main HEAD", cwd: "/repo/feature", wantPath: "/repo/feature", want: true},
		{name: "inline work tree", command: "git --work-tree=/unrelated branch -f main HEAD", cwd: "/repo/feature", wantPath: "/repo/feature", want: true},
		{name: "work tree before directory change", command: "git --work-tree /unrelated -C /repo/feature branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/feature", want: true},
		{name: "work tree after directory change", command: "git -C /repo/feature --work-tree /unrelated branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/feature", want: true},
		{name: "git directory before work tree", command: "git --git-dir /repo/main/.git --work-tree /unrelated branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/main", want: true},
		{name: "work tree before git directory", command: "git --work-tree /unrelated --git-dir /repo/main/.git branch -f main HEAD", cwd: "/tmp", wantPath: "/repo/main", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &gitStateFixture{state: testGitState()}
			fields := FieldSet{ToolInputCommand: tc.command, CWD: tc.cwd}
			got := gitRefMoveConditionMatch(fields, fixture.read)
			if got != tc.want {
				t.Fatalf("gitRefMoveConditionMatch(%q) = %v, want %v; paths = %v", tc.command, got, tc.want, fixture.paths)
			}
			if len(fixture.paths) != 1 || fixture.paths[0] != tc.wantPath {
				t.Fatalf("state paths = %v, want [%s]", fixture.paths, tc.wantPath)
			}
		})
	}
}

func TestGitRefMoveGlobalOptionValidation(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    bool
	}{
		{name: "known boolean", command: "git --no-pager branch -f main HEAD", want: true},
		{name: "known separate value", command: "git --namespace tenant branch -f main HEAD", want: true},
		{name: "known inline value", command: "git --namespace=tenant branch -f main HEAD", want: true},
		{name: "known config", command: "git -c user.name=test branch -f main HEAD", want: true},
		{name: "known config env", command: "git --config-env user.name=USER branch -f main HEAD", want: true},
		{name: "known inline exec path", command: "git --exec-path=/tmp branch -f main HEAD", want: true},
		{name: "unknown option", command: "git --not-a-real-option branch -f main HEAD", want: false},
		{name: "malformed inline value", command: "git --namespace= branch -f main HEAD", want: false},
		{name: "malformed attached config", command: "git -cuser.name=test branch -f main HEAD", want: false},
		{name: "malformed boolean value", command: "git --no-pager=value branch -f main HEAD", want: false},
		{name: "missing separate value", command: "git --namespace", want: false},
		{name: "terminal version", command: "git --version branch -f main HEAD", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &gitStateFixture{state: testGitState()}
			fields := FieldSet{ToolInputCommand: tc.command, CWD: "/repo/feature"}
			if got := gitRefMoveConditionMatch(fields, fixture.read); got != tc.want {
				t.Fatalf("gitRefMoveConditionMatch(%q) = %v, want %v", tc.command, got, tc.want)
			}
		})
	}
}

func TestGitRefMoveSelectedSourceResolvesHeadDestination(t *testing.T) {
	sourceState := gitbranch.State{
		PrimaryCheckout: "/repo/source",
		CurrentWorktree: "/repo/source",
		CurrentBranch:   "main",
		Worktrees:       []gitbranch.Worktree{{Path: "/repo/source", Branch: "main", IsPrimary: true}},
	}
	cwdState := gitbranch.State{
		PrimaryCheckout: "/repo/cwd",
		CurrentWorktree: "/repo/cwd",
		CurrentBranch:   "absent",
		Worktrees:       []gitbranch.Worktree{{Path: "/repo/cwd", Branch: "absent", IsPrimary: true}},
	}
	destinationState := testGitState()
	reader := func(path string) (gitbranch.State, error) {
		switch path {
		case "/repo/source":
			return sourceState, nil
		case "/repo/cwd":
			return cwdState, nil
		default:
			return destinationState, nil
		}
	}
	fields := FieldSet{
		ToolInputCommand: "git --git-dir=/repo/source/.git push /repo/main HEAD",
		CWD:              "/repo/cwd",
	}
	if !gitRefMoveConditionMatch(fields, reader) {
		t.Fatal("selected source repository HEAD destination did not match")
	}
}

func TestGitRefMoveAllRequiresProvenSourceBranch(t *testing.T) {
	sourceState := gitbranch.State{
		PrimaryCheckout: "/repo/source",
		CurrentWorktree: "/repo/source",
		CurrentBranch:   "topic",
		LocalBranches:   []string{"topic"},
		Worktrees:       []gitbranch.Worktree{{Path: "/repo/source", Branch: "topic", IsPrimary: true}},
	}
	destinationState := gitbranch.State{
		PrimaryCheckout: "/repo/main",
		CurrentWorktree: "/repo/main",
		CurrentBranch:   "main",
		Worktrees:       []gitbranch.Worktree{{Path: "/repo/main", Branch: "main", IsPrimary: true}},
	}
	reader := func(path string) (gitbranch.State, error) {
		if path == "/repo/source" {
			return sourceState, nil
		}
		return destinationState, nil
	}
	allFields := FieldSet{ToolInputCommand: "git push --all /repo/main", CWD: "/repo/source"}
	if gitRefMoveConditionMatch(allFields, reader) {
		t.Fatal("push --all matched a destination-only checked-out branch")
	}
	mirrorFields := FieldSet{ToolInputCommand: "git push --mirror /repo/main", CWD: "/repo/source"}
	if !gitRefMoveConditionMatch(mirrorFields, reader) {
		t.Fatal("push --mirror did not match a destination-only checked-out branch")
	}

	sourceState.LocalBranches = []string{"main", "topic"}
	if !gitRefMoveConditionMatch(allFields, reader) {
		t.Fatal("push --all did not match an un-checked-out source branch checked out at the destination")
	}
}

func TestGitRefMoveLocalPushResolvesHeadDestination(t *testing.T) {
	sourceState := testGitState()
	sourceState.CurrentWorktree = "/repo/source"
	sourceState.CurrentBranch = "main"
	destinationState := testGitState()
	paths := make([]string, 0, 2)
	reader := func(path string) (gitbranch.State, error) {
		paths = append(paths, path)
		if path == "/repo/source" {
			return sourceState, nil
		}
		return destinationState, nil
	}
	fields := FieldSet{ToolInputCommand: "git push /repo/main HEAD", CWD: "/repo/source"}
	if !gitRefMoveConditionMatch(fields, reader) {
		t.Fatalf("symbolic HEAD destination did not match; state paths = %v", paths)
	}
}

func TestBranchMoveTargetsForceCopyClustersParseCleanly(t *testing.T) {
	for _, cluster := range []string{"-fc", "-cf", "-fC", "-Cf"} {
		words := []shelldecomp.Word{
			{Value: cluster, Resolvable: true},
			{Value: "main", Resolvable: true},
			{Value: "copied", Resolvable: true},
		}
		mode, force, positionals, valid := parseBranchMoveArgs(words)
		if !valid || mode != branchMoveCopy || !force {
			t.Fatalf("parseBranchMoveArgs(%q) = %v, %v, %v, %v", cluster, mode, force, positionals, valid)
		}
	}
}

func TestLocalPushRecognizedOptionTables(t *testing.T) {
	for _, option := range pushBooleanOptions {
		flag := "--" + option.longName
		words := pushWords(flag)
		if option.effect == pushEffectAllBranches || option.effect == pushEffectMirror {
			words = pushBulkWords(flag)
		}
		if _, _, _, _, valid := parseLocalPushArgs(words); !valid {
			t.Errorf("parseLocalPushArgs rejected boolean %q", flag)
		}
		if option.shortName != 0 {
			shortFlag := "-" + string(option.shortName)
			if _, _, _, _, valid := parseLocalPushArgs(pushWords(shortFlag)); !valid {
				t.Errorf("parseLocalPushArgs rejected short boolean %q", shortFlag)
			}
		}
	}

	for _, option := range pushValueOptions {
		value := "value"
		if len(option.allowedValues) > 0 {
			value = option.allowedValues[0]
		}
		if option.allowBare {
			flag := "--" + option.longName
			if _, _, _, _, valid := parseLocalPushArgs(pushWords(flag)); !valid {
				t.Errorf("parseLocalPushArgs rejected bare value option %q", flag)
			}
		}
		if option.allowSeparate {
			flag := "--" + option.longName
			if _, _, _, _, valid := parseLocalPushArgs(pushWords(flag, value)); !valid {
				t.Errorf("parseLocalPushArgs rejected separate value %q", flag)
			}
		}
		if option.allowInline {
			flag := "--" + option.longName + "=" + value
			if _, _, _, _, valid := parseLocalPushArgs(pushWords(flag)); !valid {
				t.Errorf("parseLocalPushArgs rejected inline value %q", flag)
			}
		}
		if option.shortName != 0 {
			shortFlag := "-" + string(option.shortName)
			if _, _, _, _, valid := parseLocalPushArgs(pushWords(shortFlag, value)); !valid {
				t.Errorf("parseLocalPushArgs rejected short value %q", shortFlag)
			}
		}
	}
}

func TestLocalPushTagsGrammar(t *testing.T) {
	repository, refspecs, _, _, valid := parseLocalPushArgs(pushBulkWords("--tags"))
	if !valid || repository != "/repo/main" || len(refspecs) != 0 {
		t.Fatalf("tags-only push = %q, %v, valid %v", repository, refspecs, valid)
	}
	for _, options := range [][]string{
		{"--tags", "--all"},
		{"--all", "--tags"},
		{"--tags", "--branches"},
		{"--tags", "--mirror"},
		{"--mirror", "--tags"},
		{"--delete", "--tags"},
		{"--tags", "--delete"},
	} {
		if _, _, _, _, valid := parseLocalPushArgs(pushBulkWords(options...)); valid {
			t.Errorf("parseLocalPushArgs accepted conflicting options %v", options)
		}
	}
	if _, _, _, _, valid := parseLocalPushArgs(pushBulkWords("--tags", "--all", "--no-tags")); !valid {
		t.Fatal("parseLocalPushArgs rejected tags disabled before final bulk mode")
	}
	if _, _, _, _, valid := parseLocalPushArgs(pushWords("--delete", "--tags", "--no-tags")); !valid {
		t.Fatal("parseLocalPushArgs rejected delete after tags was disabled")
	}
}

func TestLocalPushRecognizedNegatedBooleanOptions(t *testing.T) {
	negatable := make([]string, 0)
	for _, option := range pushBooleanOptions {
		if option.negatable {
			negatable = append(negatable, option.longName)
		}
	}
	for _, option := range pushValueOptions {
		if option.negatable {
			negatable = append(negatable, option.longName)
		}
	}
	for _, name := range negatable {
		flag := "--no-" + name
		if _, _, _, _, valid := parseLocalPushArgs(pushWords(flag)); !valid {
			t.Errorf("parseLocalPushArgs rejected negated option %q", flag)
		}
	}
}

func TestLocalPushShortClustersApplyEffects(t *testing.T) {
	cases := []struct {
		cluster string
		deleted bool
		dryRun  bool
	}{
		{cluster: "-fd", deleted: true},
		{cluster: "-df", deleted: true},
		{cluster: "-fn", dryRun: true},
		{cluster: "-nf", dryRun: true},
	}
	for _, tc := range cases {
		_, _, deleted, dryRun, valid := parseLocalPushArgs(pushWords(tc.cluster))
		if !valid || deleted != tc.deleted || dryRun != tc.dryRun {
			t.Errorf("parseLocalPushArgs(%q) = delete %v, dry-run %v, valid %v", tc.cluster, deleted, dryRun, valid)
		}
	}
}

func pushWords(options ...string) []shelldecomp.Word {
	values := append(options, "/repo/main", "HEAD:refs/heads/main")
	return pushTestWords(values)
}

func pushBulkWords(options ...string) []shelldecomp.Word {
	values := append(options, "/repo/main")
	return pushTestWords(values)
}

func pushTestWords(values []string) []shelldecomp.Word {
	words := make([]shelldecomp.Word, 0, len(values))
	for _, value := range values {
		words = append(words, shelldecomp.Word{Value: value, Resolvable: true})
	}
	return words
}

func TestGitRefMoveConditionStateFailureFailsOpen(t *testing.T) {
	fixture := &gitStateFixture{err: errors.New("state unavailable")}
	fields := FieldSet{ToolInputCommand: "git branch -f main HEAD", CWD: "/repo/feature"}
	if gitRefMoveConditionMatch(fields, fixture.read) {
		t.Fatal("state-reader failure matched")
	}
}

func testGitState() gitbranch.State {
	return gitbranch.State{
		PrimaryCheckout: "/repo/main",
		DefaultBranch:   "main",
		CurrentWorktree: "/repo/feature",
		CurrentBranch:   "feature",
		LocalBranches:   []string{"feature", "main", "release/v1"},
		Worktrees: []gitbranch.Worktree{
			{Path: "/repo/main", Branch: "main", IsPrimary: true},
			{Path: "/repo/feature", Branch: "feature"},
			{Path: "/repo/release", Branch: "release/v1"},
			{Path: "/repo/detached", Branch: ""},
		},
	}
}

func loadGitCondition(t *testing.T, kind string, conditionBody string) *config.Condition {
	t.Helper()
	body := `
[[rules]]
name = "git condition"
events = ["AnyEvent"]
violation_message = "matched"

[[rules.conditions]]
kind = "` + kind + `"
` + conditionBody + "\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting() error = %v", err)
	}
	return &cfg.Rules[0].Conditions[0]
}

func loadGitRule(t *testing.T, conditions string) *config.Rule {
	t.Helper()
	body := `
[[rules]]
name = "git condition"
events = ["AnyEvent"]
violation_message = "matched"
` + conditions + "\n"
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	cfg, err := config.LoadExisting(path)
	if err != nil {
		t.Fatalf("LoadExisting() error = %v", err)
	}
	return &cfg.Rules[0]
}
