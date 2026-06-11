# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Background

agent-gate is a daemon that checks every shell command an AI agent attempts to run, and it blocks any command that violates a configured rule.

A rule can confirm its decision by running an external script. agent-gate calls this script an exec validator, and it passes context to the script through environment variables.

The rule `grep-code-use-semantic-search` blocks grep-style searches on a codebase that lm-semantic-search has indexed, so the agent uses semantic search instead. The validator script for this rule lives at `~/.config/agent-gate/validators/lm-semantic-search-indexed.sh`.

The daemon learns which directory a command runs in by parsing the command with shelldecomp, the shell parser in the gksyntax library. Some `cd` targets cannot be resolved by parsing, such as `cd "$(echo /tmp)"`. For those, the parser returns a marker string in place of a path.

The marker is `"\x00UNRESOLVABLE"`, defined in `third_party/gksyntax/shelldecomp/node.go:137`. It begins with a NUL byte because no real path can contain one, so the marker can never collide with a real path.

## Problem

The daemon copies the directory value into the `AGENT_GATE_CWD` environment variable for the validator. When that value is the marker, the variable contains a NUL byte.

Go refuses to start a process whose environment contains a NUL byte, so the validator never runs. The rule is configured to allow on error, so the daemon allows the command.

The result is that any grep behind an unresolvable `cd` skips the gate.

Three smaller defects belong to the same fix:

- Command text can carry a NUL byte of its own. It lands in `AGENT_GATE_COMMAND` and stops the validator the same way.
- The marker is written, NUL byte included, into the `effective_cwd` column of the `intake_events` table in SQLite.
- The validator runs under a 1500 ms budget. It needs about 21 ms when lm-semantic-search is healthy, but it times out and the command is allowed when lm-semantic-search is busy.

The marker travels through three files. `internal/rules/engine.go` computes it, `internal/rules/exec_gate.go` packs it into the validator input, and `internal/rules/concerns/exec/concern.go` writes it into the environment.

## Evidence

Measured on 2026-06-10 against the live deployment:

- The daemon log `~/.local/state/agent-gate/agent-gate.jsonl` holds 56 "environment variable contains NUL" entries and 29 "exec validator timed out" entries for this rule. Every one allowed the command.
- The `intake_events` table holds 483 rows with the marker in `effective_cwd`.
- The command `cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/lmd` reproduced the failure three times in a row.

The bug hides behind a cache. The daemon caches validator verdicts per search target and serves them even after they expire, so a target that was checked once keeps blocking from the cache. A new target behind an unresolvable `cd` passes through, which makes enforcement look intermittent rather than absent.

## Design

The fix has three parts. The marker keeps its value and its meaning inside the rule engine; the fix stops it at the process boundaries.

### Part 1: keep NUL out of the validator environment

The validator input builder in `internal/rules/exec_gate.go` converts the marker to an empty string. An empty `AGENT_GATE_CWD` already means "directory unknown" in the validator contract.

As a backstop, the environment builder in `internal/rules/concerns/exec/concern.go` strips NUL bytes from every value it returns. The backstop also covers NUL bytes embedded in command text. A unit test locks in the invariant.

### Part 2: keep the marker out of the database

The intake writer in `internal/daemon/server.go` converts the marker to an empty string before saving `effective_cwd`. An empty value already means "unknown" in that column. Existing rows are left as they are.

### Part 3: give the validator enough time

The rule receives `timeout_ms = 4000` in `~/.config/agent-gate/config.toml`, the maximum the configuration allows. Allow-on-error stays in place, because blocking on error would stop every grep whenever lm-semantic-search is down.

## Testing

- An environment builder test asserts that no returned value contains a NUL byte, with the marker as the working directory and with a NUL byte in the command text.
- A validator input builder test asserts that the marker becomes an empty working directory.
- A rules-package test asserts that a grep behind an unresolvable `cd`, against a stubbed indexed target, starts the validator and blocks.
- An intake test asserts that the marker is stored as an empty `effective_cwd`.

After deployment, rerunning the reproduction against a target with no cached verdict should produce a block and no NUL warning in the log.

## Out of scope

Detection expansion has its own spec: treating sed, awk, and perl or python one-liners as code search, with shelldecomp recursively parsing heredocs and interpreter `-c` scripts in place of the token regex.
