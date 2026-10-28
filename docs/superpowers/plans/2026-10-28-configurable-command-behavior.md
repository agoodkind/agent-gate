# Configurable command behavior implementation plan

> Use `subagent-driven-development` for independent tasks and `executing-plans` for integration. Track steps with checkbox syntax.

**Goal:** Correct the reported target and directory errors through reusable configuration capabilities, migrate the identified policy tables, and verify live behavior before merging.

**Architecture:** Keep syntax parsing and common file operations in shared code. Configuration supplies command descriptions, selector policy, and enforcement decisions. Conditions use their own descriptors without suppressing independent structural or analyzed facts.

**Tech Stack:** Go, TOML, tree-sitter through gksyntax, and the existing Agent Gate hook and audit interfaces.

**Spec:** [Configuration design](../specs/2026-10-28-configurable-command-behavior-design.md).

## Global constraints

- Preserve existing work and unrelated submodule changes.
- Do not add automated tests or operate Clyde.
- Run supported existing checks and evaluate hook payloads without executing their described mutations.
- Let each rule configure unresolved argument handling. Do not introduce a global block or allow policy.
- Keep common operation detection reusable and configurable. Do not require configuration to reproduce shell grammar or language syntax.
- Deploy matching executable and configuration only after direct validation.
- Record about five minutes of live activity before deployment and about five minutes afterward. Compare block rates, rules, false blockers, and errors.
- Create a ready PR, satisfy the active GitHub merge contract, merge, and classify this task's artifacts before cleanup.

## Review focus

- Program text and backup suffixes must not become file targets.
- A configured command must not suppress its redirection or analyzed embedded writes.
- A relative `cd` must apply once when a validator parses the command itself.
- A condition must not use another condition's descriptor or exemption.
- Live validation must retain checkout protections and identify changed or missing evaluations.

## Task 1: Shared argument descriptions and write targets

Modify [write specifications](../../../internal/config/write_specs.go), add `internal/config/argument_specs.go` and `internal/rules/concerns/shellargs`, and update the existing shellwrite extractor. Keep the argument parser independent of executable names.

Interfaces:

- `config.ArgumentSpec` supplies option value requirements and accepted forms, clusters, subcommand selection, positional boundaries, program exclusions, activation, and unknown-option interpretation.
- `ShellWriteSpec.Arguments` and `ShellReadSpec.Arguments` reference the same argument representation.
- `shellargs.Parse(command shelldecomp.Command, spec config.ArgumentSpec) Result` reports operands, option values, directory, selection, and unresolved state.
- `ExtractWriteTargetsWithSpecs` preserves its public signature and uses explicit descriptors for configurable command arguments.

- [ ] Implement and validate the shared schema and parser.
- [ ] Replace the temporary Perl/Ruby patch and independent editor table with configured descriptions.
- [ ] Preserve structural and analyzed target provenance when descriptors replace argument interpretation.
- [ ] Pass descriptors into local embedded extraction and `shell_write` evaluation.
- [ ] Verify quoted, attached, clustered, optional, unresolved, and `--` cases through direct hook inputs.
- [ ] Commit this coherent implementation after integration checks.

## Task 2: Other selector configuration

Modify [read target evaluation](../../../internal/rules/fields_read_targets.go), [condition selectors](../../../internal/rules/fields_condition.go), the existing shellread extractor, and the relevant prose and redirection extraction functions. Add configuration types in focused files and integrate their fields and validation in the existing Condition schema.

Interfaces:

- Conditions select enumeration descriptors independently from `search_tools` and explicitly enable recursive globs.
- Conditions declare prose path patterns and command prose values.
- Conditions declare ignored redirection paths and file descriptors.
- Condition-specific selector evaluation uses only that condition's policy.
- Shared argument descriptions support configured reads, prose values, and enumeration activation.

- [ ] Extract recursive discovery policy into descriptors, including shallow-depth exemptions.
- [ ] Extract prose extensions and command options into explicit configuration data.
- [ ] Extract redirection exemptions into explicit configuration data.
- [ ] Migrate matching installed and shipped declarations without weakening unrelated rules.
- [ ] Verify independent selection and preserved outcomes through direct hook evaluation.
- [ ] Commit the integrated selector changes.

## Task 3: Validator directory selection

Modify [field selectors](../../../internal/config/selector.go), [field access](../../../internal/rules/fields.go), and the configuration reference. Add `base_cwd` using `FieldSet.BaseCWD()` with its existing precedence. Migrate the installed Git command validator declarations from `cwd` to `base_cwd`.

- [ ] Expose the selector without changing `cwd` or `effective_cwd` semantics.
- [ ] Verify explicit workdirs, transcript recovery, missing separate workdirs, and relative `cd` commands.
- [ ] Confirm linked staging passes and primary staging still blocks without staging any files.
- [ ] Commit source and reference changes.

## Task 4: Integration and deployment

- [ ] Inspect every migrated policy against the installed baseline and preserve a private recovery copy.
- [ ] Run `make build` and `make test`. The current build system rejects `make check` and directs use of `make build`.
- [ ] Evaluate the original Perl input and the linked-worktree staging input with candidate code and configuration.
- [ ] Verify representative protected targets still block and temporary targets pass.
- [ ] Review the complete diff independently and fix scoped findings.
- [ ] Capture the final before-deployment audit window and executable/configuration identities.
- [ ] Deploy through `make deploy`, select validated configuration, and verify reload and daemon identity.
- [ ] Capture the after-deployment audit window. Inspect observed blocks, normalize block counts by evaluated activity, and investigate increases, new false blockers, and errors.
- [ ] Preserve a written comparison with exact time windows and evidence.

## Task 5: Pull request, merge, and cleanup

- [ ] Create and attach a ready PR with the full change and validation results.
- [ ] Read the active ruleset, satisfy checks and review requirements, and resolve verified findings.
- [ ] Merge without admin bypass and verify the merged commit.
- [ ] Verify the installed executable matches the accepted implementation.
- [ ] Use cleanup-git for this task's branch, worktree, and temporary artifacts. Preserve unique work and shared submodule state.
