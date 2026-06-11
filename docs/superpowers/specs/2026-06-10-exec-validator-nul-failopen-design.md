# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Background

agent-gate is a daemon that checks every shell command an AI agent attempts to run and blocks any command that violates a configured rule. A rule can confirm its decision by running an external script, which agent-gate calls an exec validator, and the daemon passes context to that script through environment variables.

The rule named `grep-code-use-semantic-search` blocks grep-style searches against any codebase that lm-semantic-search has indexed, so that the agent uses semantic search instead. Its validator script lives at `~/.config/agent-gate/validators/lm-semantic-search-indexed.sh` and asks lm-semantic-search whether any of the search's target paths are indexed.

To know which directory a command runs in, the daemon parses the command with shelldecomp, the shell parser in the gksyntax library. Some `cd` targets cannot be resolved by parsing alone, such as `cd "$(echo /tmp)"`, and for those the parser returns a marker string in place of a path. The marker is `"\x00UNRESOLVABLE"`, defined in `third_party/gksyntax/shelldecomp/node.go:137`, and it begins with a NUL byte because no real path can contain one, which guarantees the marker never collides with a real path.

## Problem

The daemon copies the directory value into the `AGENT_GATE_CWD` environment variable for the validator. When that value is the marker, the variable contains a NUL byte, and Go refuses to start any process whose environment contains one. The validator therefore never runs, and because the rule is configured to allow on error (`on_error = "open"`), the daemon allows the command. As a result, any grep that follows an unresolvable `cd` passes through the gate unchecked.

Three further defects belong to the same fix. Command text can carry a NUL byte of its own, which lands in the `AGENT_GATE_COMMAND` variable and stops the validator in the same way. The marker, NUL byte included, is also written to the `effective_cwd` column of the `intake_events` table in SQLite. Finally, the validator runs under a 1500 ms budget even though it completes in about 21 ms when lm-semantic-search is healthy, so when lm-semantic-search is busy the validator times out and the command is allowed.

The marker travels through three files: `internal/rules/engine.go` computes it, `internal/rules/exec_gate.go` packs it into the validator input, and `internal/rules/concerns/exec/concern.go` writes it into the environment.

## Evidence

These measurements were taken on 2026-06-10 against the live deployment.

The daemon log at `~/.local/state/agent-gate/agent-gate.jsonl` contains 56 entries reading "environment variable contains NUL" and 29 entries reading "exec validator timed out" for this rule, and every one of them allowed the command. The `intake_events` table contains 483 rows with the marker in `effective_cwd`. The command `cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/lmd` reproduced the failure three times in a row.

The bug hides behind a cache. The daemon caches validator verdicts per search target and serves them even after they expire, so a target that was checked once keeps blocking from the cache while a new target behind an unresolvable `cd` passes through. This is why enforcement appears intermittent rather than absent.

## Design

The fix has three parts. The marker keeps its value and its meaning inside the rule engine, which depends on it never equaling a real path; the fix stops it at the process boundaries instead.

The first part keeps NUL out of the validator environment. The validator input builder in `internal/rules/exec_gate.go` converts the marker to an empty string, and an empty `AGENT_GATE_CWD` already means "directory unknown" in the validator contract. As a backstop, the environment builder in `internal/rules/concerns/exec/concern.go` strips NUL bytes from every value it returns, which also covers NUL bytes embedded in command text. A unit test locks in the invariant that no environment value contains a NUL byte.

The second part keeps the marker out of the database. The intake writer in `internal/daemon/server.go` converts the marker to an empty string before saving `effective_cwd`, where an empty value already means "unknown". Existing rows are left as they are.

The third part gives the validator enough time to finish. The rule receives `timeout_ms = 4000` in `~/.config/agent-gate/config.toml`, the maximum the configuration allows. Allow-on-error stays in place, because blocking on error would stop every grep whenever lm-semantic-search is down.

## Testing

A unit test on the environment builder asserts that no returned value contains a NUL byte, exercised with the marker as the working directory and with a NUL byte in the command text. A unit test on the validator input builder asserts that the marker becomes an empty working directory. A rules-package test asserts that a grep behind an unresolvable `cd`, run against a stubbed indexed target, starts the validator and blocks. An intake test asserts that the marker is stored as an empty `effective_cwd`.

After deployment, rerunning the reproduction against a target with no cached verdict should produce a block and no NUL warning in the log.

## Out of scope

Detection expansion is covered by a separate spec: treating sed, awk, and perl or python one-liners as code search, with shelldecomp recursively parsing heredocs and interpreter `-c` scripts in place of the token regex.
