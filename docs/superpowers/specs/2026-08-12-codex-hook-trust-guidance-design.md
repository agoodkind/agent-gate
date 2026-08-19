# Codex hook trust guidance design

Agent Gate will explain how to trust its Codex hooks after every successful
Codex hook installation.

## Preserve the configuration boundary

The Codex installer continues to manage inline hooks in
`~/.codex/config.toml`. It does not read, write, migrate, or remove the legacy
`~/.codex/hooks.json` file.

Agent Gate does not write Codex trust hashes. Codex remains responsible for
showing the current hook definition and recording the user's trust decision.

## Print trust instructions

After writing Codex hooks, the installer prints separate instructions for the
Codex Desktop app and Codex command line interface.

Desktop instructions direct the user to open `Settings > Hooks`, reload the
hook list, open `User config`, and click `Trust` for each Agent Gate hook that
Codex marks as new or changed.

Command line instructions direct the user to restart Codex, run `/hooks`, open
each event containing an Agent Gate hook, and press `t` to trust each hook.

The message links to the official OpenAI hook documentation at
`https://developers.openai.com/codex/hooks/`.

The installer prints this guidance after every successful Codex installation,
including idempotent repair runs. It does not print the guidance when Codex is
excluded from the selected providers or when the Codex write fails.

## Verify the behavior

Existing Codex installation tests continue to verify the managed
`config.toml` block.
