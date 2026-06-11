# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Background

agent-gate is a daemon that checks every shell command an AI agent tries to run. It blocks commands that break a rule.

Some rules confirm a block by running an external script. That script is called an exec validator. The daemon passes context to the script through environment variables.

The rule `grep-code-use-semantic-search` blocks grep-style searches on codebases that lm-semantic-search has indexed. Its validator script is `~/.config/agent-gate/validators/lm-semantic-search-indexed.sh`.

The daemon works out the working directory of a command by parsing it with shelldecomp, the shell parser in the gksyntax library. When the parser cannot resolve a `cd` target, for example `cd "$(echo /tmp)"`, it returns the sentinel string `"\x00UNRESOLVABLE"`. The sentinel starts with a NUL byte so no real path can ever equal it. It is defined in `third_party/gksyntax/shelldecomp/node.go:137`.

## Problem

The daemon puts the working directory into the `AGENT_GATE_CWD` environment variable. When the value is the sentinel, the variable contains a NUL byte. Go refuses to start a process whose environment contains a NUL byte. The validator never starts. The rule sets `on_error = "open"`, so the daemon allows the command.

Result: any grep with an unresolvable `cd` skips the gate.

Three related defects share the fix:

- Command text can carry its own NUL byte. It reaches the `AGENT_GATE_COMMAND` variable and kills the spawn the same way.
- The daemon writes the sentinel, NUL included, into the `effective_cwd` column of the `intake_events` table in SQLite.
- The validator gets a 1500 ms budget. It needs about 21 ms when lm-semantic-search is healthy, but it times out and fails open when lm-semantic-search is busy.

The sentinel path through the code: `effectiveCwdAfterChain` (`internal/rules/engine.go`) → `fields.effectiveCWD()` → `buildInput` (`internal/rules/exec_gate.go`) → `BuildRequest` (`internal/rules/concerns/exec/concern.go`) → environment.

## Evidence (measured 2026-06-10)

- The log `~/.local/state/agent-gate/agent-gate.jsonl` has 56 "environment variable contains NUL" entries and 29 "exec validator timed out" entries for this rule. All fail open.
- The `intake_events` table has 483 rows with the sentinel in `effective_cwd` (hex `00554E5245534F4C5641424C45`).
- `cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/lmd` reproduced the NUL failure three times in a row.
- The bug hides because the daemon caches validator verdicts per search target and serves expired entries. A target validated once keeps blocking from cache. A cold target with an unresolvable `cd` passes through.

## Design

Three parts. The sentinel itself does not change; the rule engine depends on it internally. The fix stops it from escaping the process.

**Part 1: keep NUL out of the validator environment.**
`buildInput` maps the sentinel to the empty string. Empty already means "working directory unknown" in the validator contract. `BuildRequest` additionally strips NUL bytes from every environment value it returns. That covers every NUL source, including NUL in command text. A unit test pins the invariant.

**Part 2: keep the sentinel out of the database.**
The intake write in `internal/daemon/server.go` maps the sentinel to the empty string before storing `effective_cwd`. Empty already means "unknown" there. Existing rows stay as they are.

**Part 3: give the validator enough time.**
The rule gets `timeout_ms = 4000` in `~/.config/agent-gate/config.toml`, the maximum allowed. `on_error = "open"` stays, because failing closed would block every grep whenever lm-semantic-search is down.

## Testing

- `BuildRequest` unit test: no returned environment value contains NUL, tested with a sentinel cwd and with NUL in command text.
- `buildInput` unit test: a sentinel cwd produces an empty `EffectiveCWD`.
- Rules test: a command with an unresolvable `cd` plus a grep on a stubbed indexed target starts the validator and blocks.
- Intake test: a record with the sentinel stores an empty `effective_cwd`.
- Post-deploy: rerun the reproduction on an uncached target and confirm a block with no NUL warning in the log.

## Out of scope

A second spec covers detection expansion: treating sed, awk, and perl or python one-liners as code search, using shelldecomp's recursive parsing of heredocs and interpreter `-c` scripts instead of the token regex.
