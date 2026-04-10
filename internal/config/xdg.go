// Package config resolves XDG base directories and loads agent-gate's TOML config.
//
// Path resolution order for every configurable path:
//
//  1. Explicit value in TOML [paths] table (highest priority).
//  2. Relevant XDG env var ($XDG_CONFIG_HOME, $XDG_STATE_HOME, …).
//  3. XDG spec default (~/.config, ~/.local/state, …).
//
// The functions in this file implement steps 2 and 3.
// Step 1 is applied by the methods on Config in config.go.
package config

import (
	"os"
	"path/filepath"
)

// DefaultConfigDir returns the XDG-derived config directory for hookguard.
//
// Resolution:
//   $XDG_CONFIG_HOME/hookguard   (if $XDG_CONFIG_HOME is set)
//   ~/.config/hookguard           (XDG spec default)
//
// Note: the config file location cannot itself be overridden in TOML
// because TOML must be found before it can be read.
func DefaultConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hookguard")
}

// ConfigPath returns the full path to config.toml derived from XDG env vars.
func ConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.toml")
}

// DefaultStateDir returns the XDG-derived state directory for hookguard.
//
// Resolution:
//   $XDG_STATE_HOME/hookguard    (if $XDG_STATE_HOME is set)
//   ~/.local/state/hookguard      (XDG spec default)
//
// The audit log belongs in the state dir — not data dir — because the XDG spec
// explicitly lists "actions history (logs, history, recently used files, ...)"
// as XDG_STATE_HOME content.
func DefaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hookguard")
}

// DefaultAuditLogPath returns the XDG-derived audit log path.
// It is the fallback when no override is set in TOML.
func DefaultAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit.jsonl")
}
