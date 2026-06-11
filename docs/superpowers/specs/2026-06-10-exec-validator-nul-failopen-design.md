# Exec validator NUL fail-open fix

Date: 2026-06-10
Status: approved

## Background

agent-gate is a daemon. It checks every shell command an AI agent tries to run. It blocks commands that break a rule.

A rule can confirm a block by running an external script. agent-gate calls this script an exec validator. The daemon passes context to the script through environment variables.

One rule is `grep-code-use-semantic-search`. It blocks grep-style searches on a codebase that lm-semantic-search has indexed. The agent is told to use semantic search instead. The validator script lives at `~/.config/agent-gate/validators/lm-semantic-search-indexed.sh`.

The daemon needs to know what directory a command runs in. It parses the command with shelldecomp, the shell parser in the gksyntax library. Some `cd` targets cannot be resolved by parsing, for example `cd "$(echo /tmp)"`. For those, the parser returns a marker string instead of a path. The marker is `"\x00UNRESOLVABLE"` (`third_party/gksyntax/shelldecomp/node.go:137`). Its first byte is a NUL byte. No real path can contain a NUL byte, so the marker can never collide with a real path.

## Problem

The daemon copies the directory value into the `AGENT_GATE_CWD` environment variable for the validator. When the value is the marker, the variable contains a NUL byte. Go will not start a process with a NUL byte in its environment. So the validator never runs. The rule is set to allow on error (`on_error = "open"`). So the daemon allows the command.

In short: any grep behind an unresolvable `cd` skips the gate.

Three more defects belong to the same fix:

- Command text can contain its own NUL byte. It lands in `AGENT_GATE_COMMAND` and stops the validator the same way.
- The marker, NUL included, is written to the `effective_cwd` column of the `intake_events` table in SQLite.
- The validator gets 1500 ms to run. It needs about 21 ms when lm-semantic-search is healthy. When lm-semantic-search is busy, the validator times out and the command is allowed.

Code path of the marker: `internal/rules/engine.go` computes it, `internal/rules/exec_gate.go` packs it into the validator input, and `internal/rules/concerns/exec/concern.go` writes it into the environment.

## Evidence (measured 2026-06-10)

- The log `~/.local/state/agent-gate/agent-gate.jsonl` has 56 "environment variable contains NUL" entries and 29 "exec validator timed out" entries for this rule. Every one allowed the command.
- The `intake_events` table has 483 rows with the marker in `effective_cwd`.
- This command reproduced the failure three times in a row: `cd "$(echo /tmp)" && grep -rln "func main" /Users/agoodkind/Sites/lmd`.
- The bug hides behind a cache. The daemon caches validator verdicts per search target and serves them even after they expire. A target that was checked once keeps blocking from cache. A new target behind an unresolvable `cd` passes through.

## Design

Three parts. The marker keeps its value; the rule engine uses it internally. The fix stops it at the process boundaries.

**Part 1: no NUL in the validator environment.**
The validator input builder (`internal/rules/exec_gate.go`) turns the marker into an empty string. An empty `AGENT_GATE_CWD` already means "directory unknown" to validators. As a backstop, the environment builder (`internal/rules/concerns/exec/concern.go`) strips NUL bytes from every value. The backstop covers NUL in command text too. A unit test locks this in.

**Part 2: no marker in the database.**
The intake writer (`internal/daemon/server.go`) turns the marker into an empty string before saving `effective_cwd`. Empty already means "unknown" in that column. Old rows are left alone.

**Part 3: more time for the validator.**
The rule gets `timeout_ms = 4000` in `~/.config/agent-gate/config.toml`. That is the maximum. Allow-on-error stays, because block-on-error would stop every grep whenever lm-semantic-search is down.

## Testing

- Environment builder test: no value contains a NUL byte, with the marker as cwd and with NUL in command text.
- Input builder test: the marker becomes an empty cwd.
- Rules test: a grep behind an unresolvable `cd` against a stubbed indexed target runs the validator and blocks.
- Intake test: the marker is stored as an empty `effective_cwd`.
- After deploy: rerun the reproduction on a target with no cached verdict. Expect a block and no NUL warning in the log.

## Out of scope

Detection expansion gets its own spec: treating sed, awk, and perl or python one-liners as code search, with shelldecomp parsing heredocs and interpreter `-c` scripts recursively, replacing the token regex.
