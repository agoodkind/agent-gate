# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Problem

The `grep-code-use-semantic-search` rule fails open whenever a command contains a `cd` that shelldecomp cannot resolve. `shelldecomp.Unresolvable` is the string `"\x00UNRESOLVABLE"` (`third_party/gksyntax/shelldecomp/node.go:137`). It flows from `effectiveCwdAfterChain` (`internal/rules/engine.go`) through `fields.effectiveCWD()` into the exec gate's `buildInput` (`internal/rules/exec_gate.go`), which puts it in the `AGENT_GATE_CWD` env var (`internal/rules/concerns/exec/concern.go`). Go's `os/exec` refuses to start a process whose environment contains a NUL byte, so the validator never runs, and `on_error = "open"` allows the command.

Evidence from the live system:

- 56 log entries with `start exec validator: exec: environment variable contains NUL` and 29 with `exec validator timed out` for this rule in `~/.local/state/agent-gate/agent-gate.jsonl`, all fail-open.
- 483 rows in `intake_events` with a NUL byte in `effective_cwd` (hex `00554E5245534F4C5641424C45`).
- Reproduced three times on 2026-06-10: `cd "$(echo /tmp)" && grep -rln ... /Users/agoodkind/Sites/lmd` produced a fresh NUL spawn failure at the event timestamp. The command still appeared blocked because the stale-while-revalidate verdict cache served an old Block verdict while the background refresh failed.
- A separate NUL source also exists: a command whose text itself contains a NUL byte (reproduced with sqlite probe commands carrying a literal NUL).

The exec result cache never evicts, so paths validated once keep blocking from cache while refreshes silently fail. This masks the bug and makes enforcement look intermittent.

## Design

Three parts, all in agent-gate. The sentinel's in-engine semantics do not change: `shellread/codesearch.go`, `shellwrite/parse.go`, and project-condition matching keep relying on the NUL prefix guaranteeing the sentinel never equals a real path.

### Part 1: stop NUL from reaching the exec env (two layers)

Layer 1, semantic: in `buildInput` (`internal/rules/exec_gate.go`), map `shelldecomp.Unresolvable` to `""` for the `EffectiveCWD` path view before canonicalization. Validators then see an empty `AGENT_GATE_CWD` with `is_canonical: false`, which already means "cwd unknown" in the validator contract.

Layer 2, defensive invariant: in `BuildRequest` (`internal/rules/concerns/exec/concern.go`), strip NUL bytes from every env value before returning the slice. This is the last boundary before `os/exec` and catches every NUL source, including NUL-bearing command text. A unit test pins the invariant: no env entry returned by `BuildRequest` contains a NUL byte, for inputs that include the sentinel and a command with an embedded NUL.

### Part 2: stop the sentinel from leaking into the intake DB

At the intake write (`internal/daemon/server.go`, where `record.Operation.EffectiveCWD` is set), map the sentinel to `""`. Empty already means "unknown" in that column. Existing rows keep their historical bytes; no migration.

### Part 3: convert validator timeouts into verdicts

Set `timeout_ms = 4000` (the `MaxExecTimeoutMs` cap) on the exec condition of `grep-code-use-semantic-search` in `~/.config/agent-gate/config.toml`. The validator measures ~21ms against a healthy lm-semantic-search daemon, so the 1500ms default only trips when lms is busy. `on_error = "open"` stays: fail-closed would block every grep whenever lms is down.

## Testing

- Unit test on `BuildRequest`: env never contains NUL (sentinel cwd, NUL-bearing command).
- Unit test on `buildInput`: sentinel cwd produces an empty `EffectiveCWD` path view.
- Rules-package test: an event whose command has an unresolvable `cd` plus a grep over a stubbed indexed target runs the validator (spawn succeeds) and blocks.
- Intake test: a record with the sentinel cwd stores `effective_cwd = ""`.
- After deploy: rerun the live reproduction (`cd "$(echo /tmp)" && grep -rln "func main" <indexed repo>`) against a cold cache key and confirm a block with no NUL warning in the log.

## Out of scope (spec 2 follows)

Detection expansion: counting sed, awk, and perl/python one-liner reads of indexed repos as code search, using shelldecomp's recursive decomposition (heredoc bodies, interpreter `-c` scripts) instead of the token regex pre-filter.
