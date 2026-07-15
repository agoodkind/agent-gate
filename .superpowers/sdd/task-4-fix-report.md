# Task 4 review fix report

## Changes

- Removed the obsolete `./install.sh --hooks-only` hook reinstallation claim
  from `HOOKS.md`.
- Kept `agent-gate install hooks --bin-path "$(command -v agent-gate)"` and the
  provider opt-out variants as the hook reinstallation commands.
- Added a documentation test that allows only `--version`, `--bin-dir`, and
  `--require-attestation` on lines that document `install.sh` invocations.
  This rejects `--hooks-only`, `--service-only`, `--bin-only`, and any other
  unsupported shell installer flag.
- Replaced the unrelated Clyde stale path assertion with the shipped
  `/Users/agoodkind/.local/bin/clyde` path assertion. This catches the obsolete
  `/Users/agoodkind/.local/bin/clyde hook sessionstart` command without matching
  unrelated Clyde checkout paths.

## RED evidence

Command:

```text
go test . -run 'Test(FirstPartyDocumentationRejectsStaleClaims|DocumentedShellInstallerFlagsAreSupported)$' -count=1
```

Result before editing `HOOKS.md`: exit 1.

```text
--- FAIL: TestDocumentedShellInstallerFlagsAreSupported (0.00s)
    documentation_test.go:82: HOOKS.md:65 documents unsupported install.sh flag "--hooks-only"
FAIL
FAIL    goodkind.io/agent-gate    0.720s
```

## GREEN evidence

The same focused command passed after removing the obsolete claim:

```text
ok      goodkind.io/agent-gate    0.450s
```

The complete documentation package test passed:

```text
go test . -count=1
ok      goodkind.io/agent-gate    0.454s
```

## Repository verification

- `make test`: exit 0; all repository Go packages passed.
- `make lint`: exit 0; `lint-golangci`, `lint-format`, `lint-gocyclo`,
  `lint-deadcode`, and `staticcheck-extra` passed.
- `make check`: exit 0; all configured checks passed.
- `git diff --check`: exit 0 with no output.
- The first-party documentation scan found no occurrence of
  `/Users/agoodkind/.local/bin/clyde`, `--hooks-only`, `--service-only`, or
  `--bin-only`.

## Scope

The fix changes `HOOKS.md`, `documentation_test.go`, and this report. It does
not change the installer, CLI behavior, hook templates, generated files, or
vendored files.
