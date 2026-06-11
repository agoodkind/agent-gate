# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Background

agent-gate is a daemon that inspects every shell command an AI coding agent attempts and blocks commands that violate a configured rule. A rule can confirm its decision by running an external program, and agent-gate calls that program an exec validator. Before the daemon starts a validator, it hands the validator context through environment variables, including the command text and the working directory the command would run in.

The rule named `grep-code-use-semantic-search` blocks grep-style searches against a codebase that the lm-semantic-search index already covers, so the agent is redirected to semantic search. Its exec validator is the script `~/.config/agent-gate/validators/lm-semantic-search-indexed.sh`, which asks lm-semantic-search whether any search target is indexed.

The daemon computes the working directory a command ends in by parsing the command with shelldecomp, the shell-command parser in the gksyntax library. When a command changes directory to a target the parser cannot pin down, such as `cd "$(echo /tmp)"`, the parser reports the working directory as a sentinel string. That sentinel is `"\x00UNRESOLVABLE"`, defined in `third_party/gksyntax/shelldecomp/node.go:137`, and its first byte is a NUL character so that no real filesystem path can ever equal it.

## Problem

The daemon passes the working directory to the validator in the `AGENT_GATE_CWD` environment variable. When the working directory is the sentinel, that variable contains a NUL byte. The Go standard library refuses to start any process whose environment contains a NUL byte. The validator therefore never starts, and because the rule sets `on_error = "open"`, the daemon allows the command. Any grep-style search that includes an unresolvable `cd` passes through the gate unchecked.

The sentinel travels this path: `effectiveCwdAfterChain` in `internal/rules/engine.go` returns it, `fields.effectiveCWD()` forwards it, `buildInput` in `internal/rules/exec_gate.go` accepts it, and `BuildRequest` in `internal/rules/concerns/exec/concern.go` writes it into the environment.

A second NUL source exists independent of the sentinel: command text that itself contains a NUL byte reaches `BuildRequest` through the `AGENT_GATE_COMMAND` environment variable and kills the spawn the same way.

The daemon also writes the sentinel, NUL byte included, into the `effective_cwd` column of the `intake_events` table, the SQLite table that records every event the daemon receives.

A separate failure converts slow validator runs into allows. The validator completes in about 21 milliseconds when the lm-semantic-search daemon is responsive, but the rule runs it with the default budget of 1500 milliseconds, and when lm-semantic-search is busy the run exceeds that budget, times out, and fails open.

## Evidence

Measurements taken on 2026-06-10 from the live deployment:

- The daemon log `~/.local/state/agent-gate/agent-gate.jsonl` holds 56 entries reading `start exec validator: exec: environment variable contains NUL` and 29 entries reading `exec validator timed out` for this rule. Every entry records `block: false`.
- The `intake_events` table holds 483 rows whose `effective_cwd` value is the sentinel (hex `00554E5245534F4C5641424C45`).
- Running `cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/lmd` produced a fresh NUL spawn failure in the log at the event timestamp, three times in a row.
- The reproduction commands still appeared blocked because the daemon caches validator verdicts per search target and serves a cached verdict even after it expires. The cached block masked the spawn failure, so enforcement looks intermittent: a search target that was validated once keeps blocking from cache, while a cold target with an unresolvable `cd` passes through.

## Design

The fix has three parts, all inside agent-gate. The sentinel keeps its value and its meaning inside the rule engine, where three consumers (`shellread/codesearch.go`, `shellwrite/parse.go`, and project-condition matching) depend on it never equaling a real path. The fix only stops the sentinel from escaping the process.

### Part 1: keep NUL out of the validator environment

Two layers, applied together.

The first layer handles the sentinel where the validator input is assembled. `buildInput` in `internal/rules/exec_gate.go` maps the sentinel to the empty string before canonicalization. The validator then receives an empty `AGENT_GATE_CWD`, and an empty value already means "working directory unknown" in the validator contract.

The second layer enforces an invariant at the last boundary before process start. `BuildRequest` in `internal/rules/concerns/exec/concern.go` strips NUL bytes from every environment value it returns. This catches every NUL source, including NUL bytes embedded in command text. A unit test pins the invariant.

### Part 2: keep the sentinel out of the intake database

The intake write in `internal/daemon/server.go` maps the sentinel to the empty string before storing `effective_cwd`. An empty value already means "unknown" in that column. Existing rows keep their historical bytes; there is no migration.

### Part 3: give the validator enough time

The exec condition of `grep-code-use-semantic-search` in `~/.config/agent-gate/config.toml` gets `timeout_ms = 4000`, the maximum the config allows. The rule keeps `on_error = "open"`, because failing closed would block every grep whenever lm-semantic-search is down.

## Testing

- A unit test on `BuildRequest` asserts that no returned environment value contains a NUL byte, exercised with a sentinel working directory and with command text containing an embedded NUL.
- A unit test on `buildInput` asserts that a sentinel working directory produces an empty `EffectiveCWD` path view.
- A rules-package test builds an event whose command contains an unresolvable `cd` plus a grep over a stubbed indexed target, and asserts the validator starts and the rule blocks.
- An intake test asserts that a record carrying the sentinel stores an empty `effective_cwd`.
- A post-deploy check reruns the live reproduction against a search target with no cached verdict and confirms a block with no NUL warning in the log.

## Out of scope

A second spec covers detection expansion: counting sed, awk, and perl or python one-liner reads of indexed repositories as code search, using shelldecomp's recursive decomposition of heredoc bodies and interpreter `-c` scripts in place of the token regex pre-filter.
