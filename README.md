# agent-gate

Agent Gate applies one daemon-owned rule set to Claude Code, Codex, Cursor, Gemini CLI, and GitHub Copilot Chat hooks. Provider hook processes transport JSON. The supervised daemon evaluates policy and records durable state.

<!-- doc-test: skip reason=requires-release-network -->
```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/agent-gate/main/install.sh | bash
```

Start with the [Getting Started guide](docs/getting-started.md). Use the [Feature Guide](docs/features.md) for rule and query behavior. Read [Audit Storage](docs/audit-storage.md) before maintenance or upgrades.

The [Hook Inventory](HOOKS.md) lists registered events and managed templates. The [Hook Contract](docs/hook-schemas.md) defines payload and response behavior. The [Judge Architecture](docs/judge.md) explains inference evaluation. The [Contributor Guide](CONTRIBUTING.md) covers source development.
