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
