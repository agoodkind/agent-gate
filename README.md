# agent-gate

`agent-gate` applies one daemon-owned rule set to Claude Code, Codex, Cursor,
Gemini CLI, and GitHub Copilot Chat hooks. Provider hook processes only carry
JSON between the host and the daemon. The daemon detects the provider, parses
the payload, enriches rule fields, evaluates policy, renders the provider
response, and records durable state.

The installed hook inventory lives in [HOOKS.md](HOOKS.md). The payload and
response contracts live in [docs/hook-schemas.md](docs/hook-schemas.md). The LLM
judge architecture lives in [docs/judge.md](docs/judge.md).

## Install a release

The release installer downloads and verifies the selected release, installs the
binary, creates or updates the default config, installs the user service, waits
for the daemon, and installs all five hook templates.

```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
```

Use this public curl command when you do not have a source checkout.

The default installation uses these paths:

- Binary: `${XDG_BIN_HOME:-$HOME/.local/bin}/agent-gate`
- Config: `${XDG_CONFIG_HOME:-$HOME/.config}/agent-gate/config.toml`
- Operational log: `${XDG_STATE_HOME:-$HOME/.local/state}/agent-gate/agent-gate.jsonl`
- SQLite state: `${XDG_STATE_HOME:-$HOME/.local/state}/agent-gate/sqlite/audit.db`
- macOS service: `$HOME/Library/LaunchAgents/io.goodkind.agent-gate.plist`
- Linux service: `${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/agent-gate.service`

From a source checkout, use `./install.sh` with these release wrapper flags to
select a version, installation directory, or attestation requirement:

```sh
./install.sh --version v1.2.3
./install.sh --bin-dir "$HOME/.local/bin"
./install.sh --require-attestation
```

Run `make install-release ARGS='--version v1.2.3'` from a source checkout when
you want the Make wrapper around the same installer. After the binary exists,
use its install commands to repair one layer or omit selected providers:

```sh
agent-gate install all --auto-update check
agent-gate install all --no-config --no-service --no-copilot
agent-gate install hooks --no-claude --no-codex --no-cursor --no-gemini
agent-gate install service
```

## Understand enforcement

Each hook reads one JSON payload from standard input and sends the raw bytes,
provider hint, process arguments, working directory, and selected environment
signals to the supervised daemon over its Unix socket. The daemon stores the
receipt before evaluation, then applies every rule that targets the detected
provider and event. A block response includes every matching blocking rule that
the provider event can enforce.

Rules with multiple `[[rules.conditions]]` entries use all-match semantics.
Every condition must match before that rule fires. Separate rules remain
independent, so more than one rule can match the same event. Provider events
that cannot block are recorded as audit-only outcomes even when a rule declares
`action = "block"`.

Hook transport stays fail-open. A standard-input read failure, unavailable
daemon, failed daemon RPC, or recovered hook-entrypoint panic returns the hinted
provider's allow response with exit code 0. An unknown provider receives empty
standard output with exit code 0. A policy block returned by a healthy daemon is
not an availability failure, so the hook mirrors that response unchanged.

## Configure one deterministic rule

The smallest useful rule names provider events, selects payload fields, and
matches them with PCRE2. This example blocks direct `go build` calls across the
five installed providers:

```toml
[[rules]]
name = "build-through-make"
description = "Require the repository build target"
events = ["PreToolUse", "preToolUse", "beforeShellExecution", "BeforeTool"]
field_paths = ["tool_input.command", "command"]
pattern = '''(?:^|\s)go\s+build(?:\s|$)'''
action = "block"
violation_message = "Run the repository build target instead of go build directly."
```

`field_paths` uses the first non-empty selected value. `not_pattern` can exempt
values. `action = "audit"` records a match without blocking. The annotated
[config.toml.example](config.toml.example) covers condition kinds, named
inference points, evaluator roles, validator timeouts, error policy, caching,
and unresolved path behavior.

`action = "inject"` returns model-facing context through the provider event's
native response field. `action = "mutate"` replaces a supported prompt, tool
input, or tool output. Both actions reuse the same filters and conditions as
enforcement rules. `output` supplies text, `output_file` reads it relative to
the config file, and a matching exec condition replaces it with complete stdout.

## Reload and validate config

The daemon watches the directory containing `config.toml` and debounces file
events for 200 milliseconds. A valid replacement creates a new runtime snapshot
and writes `config reloaded` to the operational log. An invalid replacement
writes `config reload rejected` and leaves the active snapshot unchanged.

Validate the same load, compile, and schema checks before saving:

```sh
agent-gate config check
```

Config-only edits do not require a daemon restart.

## Inspect operational and durable state

`agent-gate.jsonl` is the rotated operational log for daemon startup, reload,
validator, inference, update, and service diagnostics. It is not the audit
record.

`sqlite/audit.db` is the durable state store. Intake rows prove that the daemon
received an event and track deferred replay. Audit rows hold derived allow,
block, and audit-only outcomes. Evaluation rows hold deterministic and inference
layers for rules that use evaluator matrices. The same database remains the
source for query and export commands.

Use `query seen` for intake, `query decisions` for derived policy outcomes, and
`query evaluations` for evaluator traces:

```sh
agent-gate query seen --today
agent-gate query seen --state pending --limit 20
agent-gate query seen --event-id intake_123 --json --include-normalized --include-env
agent-gate query decisions --since 24h --decision block
agent-gate query decisions --rule build-through-make --json
agent-gate query evaluations --since 24h --rule build-through-make
agent-gate query evaluations --evaluation-id eval_123 --json
agent-gate export evaluations --since 24h --rule build-through-make
```

`query seen` omits raw payload bodies. Its optional flags expose normalized JSON
and the environment fingerprint. `export evaluations` emits one JSONL row per
evaluation with ordered typed layers and labels.

## Operate the daemon and updater

Check the daemon identity and socket:

```sh
agent-gate daemon status
```

Reinstall the managed service around the current binary when its service file
needs repair:

```sh
agent-gate install service --bin-path "$(command -v agent-gate)"
```

Inspect or apply release updates through the daemon-owned updater:

```sh
agent-gate update check
agent-gate update apply --dry-run
agent-gate update apply
agent-gate update status
```

An applied update signals the supervised daemon and waits for the service owner
to start the new binary. Direct release installs enable apply mode by default;
`[update]` in the config controls the interval, repository, mode, and prerelease
policy.

## Build from source

Use the Go toolchain declared in `go.mod`, Git with submodule support, PCRE2,
and a working C compiler. Install `pcre2` with Homebrew on macOS or
`libpcre2-dev` on Debian and Ubuntu. The Make pipeline enables cgo, initializes
the `gksyntax` submodule, generates required parsers, and builds with the FTS5
SQLite tag.

```sh
git clone --recurse-submodules https://github.com/agoodkind/agent-gate
cd agent-gate
make build
make test
make lint
make check
```

Use these repository targets for local operation and generated sources:

```sh
make deploy
make daemon-status
make proto
```

`make deploy` installs the current signed build to the per-user binary path,
restarts the supervised service without overlapping daemon instances, waits for
readiness, and prints daemon status. Run `make check` before committing.
