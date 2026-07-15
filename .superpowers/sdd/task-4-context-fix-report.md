# Task 4 checkout context fix report

## Changes

- Identified the public curl one-liner as the release installation path for
  users without a source checkout.
- Identified the three valid `./install.sh` flag examples as commands that run
  from a source checkout.
- Kept the existing source-checkout requirement on the
  `make install-release` example.
- Preserved the `--version`, `--bin-dir`, and `--require-attestation` examples.

## Documentation test decision

No new documentation test was added. The existing
`TestDocumentedShellInstallerFlagsAreSupported` test protects the supported
flag set without depending on prose. The remaining requirement is a semantic
distinction between the public no-checkout path and local checkout commands.
An automated assertion would need to match particular wording or Markdown
adjacency, which would be brittle and would not establish that the distinction
is clear to a reader. The focused flag test plus reviewer inspection covers the
stable contract and its presentation.

## Verification

The focused documentation tests passed:

```text
go test . -run 'Test(FirstPartyDocumentationRejectsStaleClaims|DocumentedShellInstallerFlagsAreSupported|FirstPartyDocumentationLocalLinksResolve|DocumentedMakeTargetsExist|DocumentedCLICommandNamesExist|DocumentedProvidersMatchShippedTemplates)$' -count=1
ok  	goodkind.io/agent-gate	0.722s
```

- `make test`: exit 0; all repository Go packages passed.
- `make lint`: exit 0; all configured lint checks passed.
- `make check`: exit 0; all configured checks passed.

## Scope

The fix changes `README.md` and this report. It does not change installer
behavior, CLI behavior, hook templates, generated files, vendored files, or the
valid release wrapper flag set.
