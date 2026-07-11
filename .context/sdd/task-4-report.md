# Task 4 Report

## Status

Implemented the generic `git_primary_checkout` and `git_ref_move` condition kinds without changing policy, user configuration, the oracle, or gksyntax.

## RED

The focused condition test command failed before production edits because `ConditionKindGitPrimaryCheckout`, `ConditionKindGitRefMove`, `gitPrimaryCheckoutConditionMatch`, and `gitRefMoveConditionMatch` did not exist.

## GREEN

The focused config and rules suite passed after implementation:

```text
ok  goodkind.io/agent-gate/internal/config
ok  goodkind.io/agent-gate/internal/rules
```

The tests use injected `gitbranch.State` readers. They cover primary, feature, relative, command cwd, declared writer, unresolved, and state-error targets. Ref-move cases cover force, delete, rename, update-ref, checkout, switch, local push, wrappers, global flags, literal assignments, branch slashes, detached worktrees, dynamic input, malformed input, reads, remote refs, tags, and current-worktree reset exclusions.

## Verification

`make test` passed every package after the final implementation changes.

`make check` passed `lint-golangci`, `lint-format`, `lint-gocyclo`, `lint-deadcode`, and `staticcheck-extra`.

`git diff --check` produced no output.

`make daemon-status` confirmed the prior installed daemon was restored under supervision. This task did not deploy the working tree.

## Runtime Git Execution

The new conditions call `gitbranch.ReadState`, which uses go-git. A semantic production-code search for runtime command execution found the generic exec-condition runner, while git-specific command execution appeared only in existing test fixtures. The Task 4 production diff adds no `os/exec` import or runtime git process invocation.

## Self-review

The implementation keeps both conditions gate-only and preserves existing callers. `git_primary_checkout` reuses the existing target union and condition-specific `write_specs`. `git_ref_move` parses expanded shell structure, fails open on opaque, dynamic, malformed, or unreadable state, and restricts push matching to structurally local repositories and local branch destinations.

The config validator rejects `field_paths` on `git_ref_move` because that condition derives its target only from the command. The exhaustive kind switches include both new kinds.

## Commit

The final handoff records the commit SHA because a commit cannot contain its own stable hash.

## Concerns

No known correctness concerns remain within Task 4 scope. Configuration cutover, policy composition, deletion of the old validator, and live deployment remain intentionally out of scope.

## Review Fixes

### RED

`go test ./internal/rules -run TestGitRefMoveConditionMatch -count=1` reproduced 13 failures. The failures showed that rename inspected the destination instead of the source, one-positional rename claimed an unrelated checkout, incomplete update-ref forms matched, unknown short-option clusters matched, and `--repo` local push destinations were discarded.

### GREEN

The same focused command passed after the parser fixes:

```text
ok  goodkind.io/agent-gate/internal/rules  0.585s
```

The broader focused command passed `internal/config`, `internal/gitbranch`, and `internal/rules`. A final `make test` passed every package, and a final `make check` passed all five lint and static-analysis gates.

Rename now evaluates the source ref only for the two-positional form and fails open for the current-branch one-positional form. Update-ref validates normal and delete arity. Branch parsing accepts only supported short actions and combinations. Push parsing treats separate and inline `--repo` values as repository destinations while remote names and non-file URLs remain non-local.

### Final Parser Review RED

The focused parser command failed for all four valid force-rename clusters, rejected force-copy clusters before classifying them, and matched a local push carrying an unknown option.

### Final Parser Review GREEN

The focused suite passed after recognizing valid force-rename and force-copy clusters, treating copy as a parsed non-match, and validating push options against explicit structural forms:

```text
ok  goodkind.io/agent-gate/internal/rules  0.634s
```

The broader focused config, gitbranch, and rules suite passed. The post-refactor `make test` passed every package, and `make check` passed all five gates. Unknown push options now fail open, while local pushes using recognized force, delete, repository, receive-pack, and push-option forms retain their structural matches.

### Push Option Grammar RED

The installed `git push -h` grammar exposed missing valid forms for short delete, branches, progress, verify, IPv4, IPv6, and separate recurse-submodules values. Focused tests reproduced those rejected forms and showed that short and long dry-run options incorrectly matched ref movement.

### Push Option Grammar GREEN

The focused rules suite passed after adding explicit delete and dry-run state and completing the recognized boolean, value-taking, and inline-value tables:

```text
ok  goodkind.io/agent-gate/internal/rules  0.611s
```

A table-driven test covers every recognized parser-table entry. Short delete preserves local delete refspec matching, separate and inline recurse-submodules values are consumed, unknown options fail open, and dry-run produces no ref-move match. The final `make test` passed every package and `make check` passed all five gates.

### Declarative Option Hardening RED

Focused tests reproduced rejection of valid `-fd` and `-df` clusters and all declared negated options. They also reproduced overacceptance of invalid signed and recurse-submodules enum values. Direct cluster tests confirmed that delete and dry-run effects were not applied character by character.

### Declarative Option Hardening GREEN

Boolean names, short aliases, negation support, and semantic effects now live in one metadata table. Value option syntax, optionality, negation support, and closed enum values live in a second table. Metadata-driven tests exercise every entry, every short alias, every declared negation, and every supported bare, separate, and inline value form.

The focused suite passed:

```text
ok  goodkind.io/agent-gate/internal/rules  0.673s
```

Unknown short members, unknown negations, and invalid enum values fail open. Delete members in short clusters set delete semantics, dry-run members suppress matching, and later `--no-dry-run` clears that state. The final `make test` passed every package and `make check` passed all five gates.

### Signed Argument Form RED

A focused regression showed that `--signed yes` consumed `yes` as a separate value and then matched the following local repository update.

### Signed Argument Form GREEN

The signed metadata now rejects separate values while preserving bare `--signed` and validated inline `--signed=<value>` forms. The focused suite passed in `0.544s`, full `make test` passed every package, and `make check` passed all five gates.

## Follow-up Commit Review

### Pre-fix Verification

Fresh verification of commit `47e27af55dd6b307b4e29ea558233d5c2d07eb72`
passed the focused config, gitbranch, and rules suite. `make test` passed with
trace `6e6fd08671081a2493898496e92372ae`, and `make check` passed all five
gates with trace `bf6388967237a17984d51ef3168ec9c1`.

### RED

The read-only commit review found four uncovered ref-move forms. The focused
command reproduced failures for local `--all` and `--mirror` pushes, separate
and inline repository-selecting global flags, checkout and switch operands after
`--`, and a local push whose colonless `HEAD` destination requires the source
repository's current branch.

```text
go test -count=1 ./internal/rules -run 'TestGitRefMoveConditionMatch|TestGitRefMoveRepositorySelectingGlobalFlags'
```

Eight subtests failed before production edits. A separate symbolic-`HEAD`
regression failed with the destination state as the only state read. A later
self-review added both global-flag orderings and reproduced four failures around
`--work-tree` current-path handling and explicit state-path preservation.

### GREEN

The first follow-up distinguished the invocation's current worktree from the
selected state path. `--git-dir` selects repository state, and ordinary
current-worktree comparisons use the invocation cwd. Checkout and switch stop
reset-flag parsing when `--` precedes a reset flag.

Local `--all`, `--branches`, and `--mirror` pushes initially compared the
destination's registered branch worktrees. Colonless `HEAD` pushes resolve a
source current branch through the injected state reader before comparing
destination state. The second review below tightened both behaviors.

The final focused command passed:

```text
ok  goodkind.io/agent-gate/internal/rules  0.546s
```

The broader config, gitbranch, and rules suite passed. Final `make test` passed
every package with trace `f7bdaf352e9118116263d80a1c09868f`. After the
global-option ordering self-review, the final full run passed again with trace
`f7bdaf352e9118116263d80a1c09868f`, and final `make check` passed all five
gates with trace `4c74582fc4b8eecc5897cb53d1e39c9b`.

The follow-up commit SHA is recorded in the final handoff because the commit
cannot contain its own stable hash.

## Follow-up Re-review

### RED

The follow-up review found five interactions that the first fixes did not cover:

- `--work-tree` changed the repository state path even though Git retains the
  repository selected by cwd or `--git-dir`.
- Symbolic `HEAD` used the invocation cwd instead of an explicit source
  `--git-dir`.
- `--all`, `--branches`, and `--mirror` shared one negation state.
- `--all` matched destination-only branches that the source could not prove it
  would push.
- Checkout and switch returned a reset target before seeing a later `--`.

Literal injected-state tests reproduced eleven failures across these cases,
including both global-flag orders and distinct source and destination states.

### GREEN

`--work-tree` is now consumed without selecting repository state. `--git-dir`
selects source state in either order with `--work-tree`, and an explicit git
directory survives later `-C`. Symbolic `HEAD` reads the selected source state.

All/branches and mirror use independent option effects and sentinels, so each
negation changes only its own mode. All/branches matches only branch names proven
by the injected source state, while mirror can still match destination-only
branches that it may delete. Dry-run and its negation remain independent.

Checkout and switch scan the complete argument list and return no reset target
when `--` appears before or after the reset flag.

The final focused suite passed in `0.689s`. The final `make check` passed all
five gates with trace `44b866dbb3aef39821dcf0974929fb4e`, and the final
`make test` passed every package with trace
`6ee5b70f2bb80df6a03336fca7c76b7f`.

## Follow-up Final Review

### RED

The final review found two remaining parser boundaries. Local push accepted
incompatible bulk modes with explicit refspecs, with each other, or with
delete mode. Checkout and switch also accepted more than one start-point
operand after `-B` or `-C`.

Focused tests reproduced six initial failures for incompatible bulk modes and
excess reset operands. Three additional tests covered delete combined with
all, branches, and mirror modes.

### GREEN

Local push now rejects all and mirror modes when an explicit refspec is
present, rejects all combined with mirror, and rejects delete combined with a
bulk mode. The recognized-option table uses valid no-refspec command shapes
for bulk modes.

Checkout and switch now accept at most one start-point operand after a reset
target and reject a later `--` marker.

The broader config, gitbranch, and rules suite passed. The final `make check`
passed all five gates with trace `7670043b236fa082d407cd24665bf966`, and the
final `make test` passed every package with trace
`44c86e35bd5997379c5ce5fe674babe2`.

## Checkout and Switch Grammar Review

### RED

The final read-only reviews found that checkout and switch still accepted
unknown options, mutually exclusive reset modes, a switch-only option on
checkout, and incompatible force or discard mode combined with merge mode.

Literal tests reproduced matches for unknown options and `--detach` combined
with `-B` or `-C`. Later tests reproduced checkout accepting
`--discard-changes`, checkout accepting force with merge, and switch accepting
discard changes with merge.

### GREEN

The reset parser now accepts a bounded option grammar, distinguishes checkout
from switch for `--discard-changes`, and tracks force or discard mode separately
from merge mode. Unknown options, detached reset commands, cross-family
options, and force-or-discard plus merge combinations fail open.

The focused and broader config, gitbranch, and rules suites passed. Two fresh
read-only reviews reported no unresolved findings. The final `make check`
passed all five gates with trace `b1ed1ab7bc7967eea32d0c77afd5875a`, and the
final `make test` passed every package with trace
`92b31c61599f0c788dc3c02c717f5601`. The final `git diff --check`
produced no output.

## Final Follow-up Findings

### RED

The final follow-up review found three remaining fail-open gaps. `push --all`
derived source branches only from registered worktrees, `--tags` did not
conflict with bulk push modes, and unknown or malformed Git global options
were skipped before the subcommand.

Focused tests reproduced the missing un-checked-out source branch, tag
conflicts in both option orders, and unknown, terminal, or malformed global
options. A state test also required loose and packed local branches to be
returned as one sorted, deduplicated set.

### GREEN

Pure go-git state now exposes every local `refs/heads` branch, including packed
refs. All/branches compares destination worktrees with that complete source
branch set, while mirror retains destination-only deletion semantics.

Push parsing tracks tags independently and rejects tags combined with all,
branches, or mirror modes. Git invocation parsing accepts only the supported
global option grammar, consumes required separate or inline values, preserves
repository-selector ordering, and fails open on unknown or malformed options.

The final focused gitbranch, oracle, and rules suite passed. The final
`make test` passed every package with trace
`1b5a4ab79e3e83c5524ab4fd800e0e42`, and the final `make check` passed all five
gates with trace `9e0a023aa4a7fb8b3ee30a9a8e0c0d21`. The final
`git diff --check` produced no output.
