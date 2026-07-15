# Final integration fixes report

Both Important findings from the final branch review are resolved in commit `57f091e3872e7f49a7746b6e605d523f992e6636`.

## RED evidence

### Canonical executable identity

`go test ./cmd/agent-gate -run '^TestRunInstallCanonicalizesSymlinkedBinPathBeforeInstallation$' -count=1` failed for both `service` and `all`. The service input retained the real temporary symlink path instead of the executable target.

`go test ./cmd/agent-gate -run '^TestWaitForDaemonReadyCanonicalizesReportedExecutablePath$' -count=1` timed out with an identity mismatch between a symlink path and its canonical target while the build hashes matched.

`go test ./internal/install -run '^TestInstallRendersCanonicalExecutablePathFromSymlink$' -count=1` showed that the real launchd template retained the symlink path instead of the canonical executable path.

### Read-only hook planning

`go test ./internal/install -run '^TestInstallHooksMalformedLaterProviderLeavesEveryTargetUntouched$' -count=1` rejected malformed Cursor JSON only after rewriting the earlier Claude and Codex targets.

`go test ./cmd/agent-gate -run '^TestRunInstallAllAppliesPreparedHookBytesAfterServiceReadiness$' -count=1` returned exit 1 because hook installation reparsed a Cursor target changed after preflight instead of applying retained prepared bytes.

## GREEN evidence

The final focused command passed:

```text
go test ./cmd/agent-gate ./internal/install -run '^(TestRunInstallCanonicalizesSymlinkedBinPathBeforeInstallation|TestRunInstallRejectsBrokenExecutableSymlinkBeforeMutations|TestWaitForDaemonReadyCanonicalizesReportedExecutablePath|TestRunInstallAllMalformedLaterHookLeavesConfigServiceAndHooksUntouched|TestRunInstallAllAppliesPreparedHookBytesAfterServiceReadiness|TestInstallHooksMalformedLaterProviderLeavesEveryTargetUntouched|TestPrepareHookInstallationRejectsMalformedCodexManagedBlockWithoutWrites|TestInstallRendersCanonicalExecutablePathFromSymlink)$' -count=1
ok goodkind.io/agent-gate/cmd/agent-gate
ok goodkind.io/agent-gate/internal/install
```

The broken-symlink test confirms exit 2 before mutation and retains the link path, the `resolve executable symlinks` stage, and the operating system error in stderr.

The malformed-later-provider command test confirms config state, service state, and all five hook targets retain their original bytes.

The TOCTOU test confirms the apply stage uses bytes retained during preflight even if a target changes after service readiness.

## APIs and orchestration

- `CanonicalExecutablePath` resolves an executable to an absolute path with all symlinks evaluated and preserves useful resolution errors.
- `HookInstallationPlan` retains private target paths, provider labels, rendered bytes, and the output writer.
- `PrepareHookInstallation` canonicalizes the executable, reads selected templates and existing targets, parses and merges JSON, validates Codex managed markers, and returns without writing.
- `ApplyHookInstallation` writes only the bytes retained by the prepared plan.
- `install hooks` prepares and applies directly.
- `install all` prepares once before config or service mutation and applies the same plan after service readiness.
- Service validation and direct service installation canonicalize the executable before template rendering.
- Readiness hashes the canonical executable and canonicalizes the daemon-reported path before identity comparison.

Copilot remains full-file owned. Claude, Cursor, and Gemini retain their existing merge behavior. Codex retains its managed-block rewrite behavior and now rejects unmatched or duplicate managed markers during preparation.

## Files

- `cmd/agent-gate/install.go`
- `cmd/agent-gate/install_test.go`
- `internal/install/executable_path.go`
- `internal/install/install.go`
- `internal/install/install_test.go`

## Verification

- Focused affected packages: passed.
- `make test`: passed every package, trace `040e2d4f01481103811c4c8728e3f24e`.
- `make lint`: passed golangci, format, complexity, dead-code, and strict static checks, trace `181d2e5c4baa919e3455efdc3f0ceef1`.
- `make check`: passed all checks after the final orchestration refactor, trace `3c7ac9f7a5657d52468d84b29e9a92f7`.
- `git diff --check`: passed.
- Shell and documentation checks were not applicable because this fix changed only Go implementation and Go tests.

## Commit and signature

Implementation commit: `57f091e3872e7f49a7746b6e605d523f992e6636`, `Canonicalize installer paths and prepare hook writes`.

`git cat-file commit 57f091e` contains the raw `gpgsig -----BEGIN SSH SIGNATURE-----` header. The commit message contains `Co-authored-by: Codex <noreply@openai.com>` exactly.

## Concerns

No contract conflict remains. A runtime filesystem failure during plan application can still leave an earlier provider written while a later provider fails, although each individual target write remains atomic. Predictable template, parse, merge, Codex marker, and target-read failures now occur before config, service, or hook mutation.
