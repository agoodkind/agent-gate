// Package config resolves XDG base directories and loads agent-gate's
// TOML config.
//
// Path resolution order for every configurable path:
//
//  1. Explicit value in TOML [paths] table (highest priority).
//  2. Relevant XDG env var ($XDG_CONFIG_HOME, $XDG_STATE_HOME,
//     $XDG_RUNTIME_DIR, ...).
//  3. XDG spec default (~/.config, ~/.local/state, ...).
//
// The functions in this file implement steps 2 and 3. Step 1 is applied
// by the methods on Config in config.go.
package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

const appName = "agent-gate"

// DefaultConfigDir returns the XDG-derived config directory for agent-gate.
//
// Resolution:
//
//	$XDG_CONFIG_HOME/agent-gate   (if $XDG_CONFIG_HOME is set)
//	~/.config/agent-gate           (XDG spec default)
//
// Note: the config file location cannot itself be overridden in TOML
// because TOML must be found before it can be read.
func DefaultConfigDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, appName)
}

// Path returns the full path to config.toml derived from XDG env vars.
func Path() string {
	return filepath.Join(DefaultConfigDir(), "config.toml")
}

// DefaultStateDir returns the XDG-derived state directory for agent-gate.
//
// Resolution:
//
//	$XDG_STATE_HOME/agent-gate    (if $XDG_STATE_HOME is set)
//	~/.local/state/agent-gate      (XDG spec default)
//
// The audit log belongs in the state dir because the XDG spec lists logs and
// history as XDG_STATE_HOME content.
func DefaultStateDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = os.Getenv("HOME")
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, appName)
}

// DefaultConversationsDir returns the XDG-derived base directory for
// per-conversation audit logs. Each conversation gets its own subfolder
// under <state>/conversations/<system>/<session_id>/.
func DefaultConversationsDir() string {
	return filepath.Join(DefaultStateDir(), "conversations")
}

// DefaultAuditSQLitePath returns the XDG-derived path to the audit
// SQLite database.
func DefaultAuditSQLitePath() string {
	return filepath.Join(DefaultStateDir(), "sqlite", "audit.db")
}

// ProfilesConfigPath returns the path to profiles.toml in the XDG config dir.
func ProfilesConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "profiles.toml")
}

// RuntimeDir returns the XDG_RUNTIME_DIR for agent-gate.
//
// Resolution:
//
//	$XDG_RUNTIME_DIR/agent-gate   (if $XDG_RUNTIME_DIR is set, tmpfs on Linux/systemd)
//	$TMPDIR/agent-gate             (macOS fallback)
//	/tmp/agent-gate                (final fallback)
func RuntimeDir() string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); base != "" {
		return filepath.Join(base, appName)
	}
	if base := os.Getenv("TMPDIR"); base != "" {
		return filepath.Join(base, appName)
	}
	return filepath.Join("/tmp", appName)
}

// DaemonSocketPath returns the Unix socket path for the agent-gate daemon.
func DaemonSocketPath() string {
	return filepath.Join(RuntimeDir(), "daemon.sock")
}

// EnsureRuntimeDir creates the agent-gate runtime directory with the
// permissions required by the XDG spec (0700 for XDG_RUNTIME_DIR).
func EnsureRuntimeDir() error {
	log := slog.Default()
	dir := RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Error("create runtime dir failed", "dir", dir, "err", err)
		return fmt.Errorf("failed to create runtime dir %s: %w", dir, err)
	}
	return nil
}
