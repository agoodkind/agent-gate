# Feature Guide

Agent Gate sends provider hook events to one supervised daemon. The daemon records intake, resolves the provider, evaluates every targeted rule, renders the provider response, and records the durable decision.

## Target events and fields

A rule targets provider event names and selects one or more normalized fields. Multiple conditions on one rule use all-match behavior. Separate rules remain independent and can all match one event.

The [Hook Inventory](../HOOKS.md) lists installed events and provider capabilities. The [Hook Contract](hook-schemas.md) defines preserved payloads, normalized fields, and provider responses.

This rule blocks direct builds:

```toml
[[rules]]
name = "build-through-make"
events = ["PreToolUse", "preToolUse", "beforeShellExecution", "BeforeTool"]
field_paths = ["tool_input.command", "command"]
pattern = '''(?:^|\s)go\s+build(?:\s|$)'''
action = "block"
violation_message = "Run the repository build target instead."
```

This rule injects project context into supported prompts:

```toml
[[rules]]
name = "review-context"
events = ["UserPromptSubmit", "beforeSubmitPrompt"]
field_paths = ["prompt"]
pattern = '''(?i)\breview\b'''
action = "inject"
output = "Check the repository rules before proposing changes."
```

Use the annotated [configuration example](../config.toml.example) for exact keys, condition kinds, virtual fields, and evaluator options.

## Choose an action

`block` denies events that the provider can enforce. An unsupported block becomes an audit-only outcome. `audit` records a match without changing the provider response. `inject` adds model-facing context through a supported response field. `mutate` replaces a supported prompt, tool input, or tool output.

Provider capabilities determine which response effects can be applied. The daemon never fabricates an unsupported effect.

## Evaluate a rule

Deterministic conditions evaluate normalized fields in process. External validators receive the matched rule context through a child process. Inference evaluators call a configured model endpoint. Ordered evaluator roles fold their results into one rule verdict.

The [Judge Architecture](judge.md) defines structural inputs, batching, caching, verdict folding, costs, and inference limits.

Hook transport fails open when standard input, the daemon connection, or the remote call fails. These failures return the provider allow response and record visible fail-open evidence. Evaluator errors follow each evaluator's configured error policy.

## Query durable state

Use intake queries to prove receipt, decision queries to inspect policy outcomes, and evaluation queries to inspect evaluator layers.

<!-- doc-test: run fixture=query -->
```sh
agent-gate query seen --today
agent-gate query decisions --since 24h
agent-gate query evaluations --since 24h
```

Complete exports require the requested detail classes. Expired or unrecorded detail fails the export unless the command explicitly allows skipping incomplete records.

<!-- doc-test: run fixture=query -->
```sh
agent-gate export evaluations --since 24h
```

## Manage updates

The daemon can check for updates or apply them through its configured mode. An applied update signals the supervised service owner and waits for the replacement daemon.

<!-- doc-test: skip reason=requires-release-network -->
```sh
agent-gate update check
agent-gate update apply --dry-run
agent-gate update status
```

## Limits

Hooks depend on the supervised daemon for enforcement. Transport failures allow the provider action. Provider event schemas and response capabilities differ. Inference decisions depend on the configured endpoint and its evidence. Retention can remove completed detail that later exports request.
