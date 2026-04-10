package config

import (
	"os"
	"path/filepath"
)

// defaultTOML is written to the config file on first run.
const defaultTOML = `[log]
level = "debug"

# ── Path overrides (optional) ─────────────────────────────────────────────────
#
# By default agent-gate resolves all paths from XDG env vars:
#
#   audit_log  →  $XDG_STATE_HOME/agent-gate/audit.jsonl
#                 ($XDG_STATE_HOME defaults to ~/.local/state)
#
# Set a field below to an absolute path to override that resolution chain.
# Leave empty (or omit) to keep using the env var / XDG spec default.

[paths]
audit_log = ""  # example: "/var/log/agent-gate/audit.jsonl"

# ── Rules ────────────────────────────────────────────────────────────────────
#
# Each rule is evaluated in order. The first matching rule wins.
#
# Fields:
#   name              – unique identifier shown in audit log and error messages
#   description       – human-readable explanation
#   events            – hook event names this rule applies to (empty = all)
#   field_paths       – dot-path(s) into the JSON payload to inspect;
#                       first non-empty value found across all paths is tested
#   pattern           – RE2 regex matched against the extracted field value
#   action            – what to do on match: "block" is the only supported action
#   violation_message – message sent back to the AI and written to the audit log

[[rules]]
name        = "no-shell-redirection"
description = "Block shell output/error redirection patterns LLMs commonly emit to suppress errors"
# Apply only to the events where a shell command is present.
events      = ["PreToolUse", "beforeShellExecution"]
# Claude sends the command at tool_input.command; Cursor sends it at command.
field_paths = ["tool_input.command", "command"]
# Matches: 2>&1  >&2  &>  |&  >/dev/null  2>/dev/null  &>/dev/null  (and >> variants)
pattern     = '(\d+>&\d+|>&\d+|&>|\|&|>/dev/null|2>/dev/null|>>/dev/null|2>>/dev/null|&>/dev/null)'
action      = "block"
violation_message = "Shell redirection is not permitted (e.g. 2>&1, >/dev/null, &>). Express output handling explicitly without redirects."
`

// writeDefault creates the config directory and writes the default config file.
func writeDefault(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(defaultTOML), 0o644)
}
