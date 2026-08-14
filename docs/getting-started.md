# Getting Started

Agent Gate installs one supervised daemon and verifies each selected provider through durable audit records.

## Install and verify

You need macOS or Linux, `curl`, and a supported provider client. The release installer selects interactive setup when a controlling terminal is available.

<!-- doc-test: skip reason=requires-release-network -->
```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
```

Setup shows the detected clients and existing managed registrations. Select at least one provider and an audit profile. Review the preview before confirming. Success prints one verified result for each provider after the command appears in durable intake, evaluation, and audit records.

Run setup again to repair an installation. A failed stage prints a command that preserves the selected providers, profile, update mode, binary path, and service templates.

<!-- doc-test: run -->
```sh
agent-gate daemon status
```

## Add a rule

Open the installed configuration. Use the annotated [configuration example](../config.toml.example) for the complete rule contract.

Add this deterministic rule to block direct `go build` calls:

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

Validate before saving:

<!-- doc-test: run -->
```sh
agent-gate config check
```

The daemon reloads a valid saved configuration without restarting. The operational log records `config reloaded`. An invalid replacement leaves the active rules unchanged.

Trigger the rule with a harmless provider command, then inspect its durable receipt and decision:

<!-- doc-test: run fixture=query -->
```sh
agent-gate query seen --today
agent-gate query decisions --since 24h --decision block
```

## Upgrade or recover

Audit database upgrades are not supported. Follow the [Audit Storage](audit-storage.md) upgrade and retention warning before installing a new release.

<!-- doc-test: skip reason=changes-installed-release -->
```sh
agent-gate update check
agent-gate update apply
```

Use the exact repair command printed by setup when configuration, service, or hook installation fails. The command repairs only the failed prepared stage. Provider hooks remain installed when maintenance or storage setup fails.
