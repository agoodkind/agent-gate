// Package config resolves XDG base directories and loads agent-gate's TOML config.
//
// Path resolution order for every configurable path:
//
//  1. Explicit value in TOML [paths] table (highest priority).
//  2. Relevant XDG env var ($XDG_CONFIG_HOME, $XDG_STATE_HOME, $XDG_RUNTIME_DIR, …).
//  3. XDG spec default (~/.config, ~/.local/state, …).
//
// The functions in this file implement steps 2 and 3.
// Step 1 is applied by the methods on Config in config.go.
package config

import (
	"fmt"
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

// ConfigPath returns the full path to config.toml derived from XDG env vars.
func ConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "config.toml")
}

// DefaultStateDir returns the XDG-derived state directory for agent-gate.
//
// Resolution:
//
//	$XDG_STATE_HOME/agent-gate    (if $XDG_STATE_HOME is set)
//	~/.local/state/agent-gate      (XDG spec default)
//
// The audit log belongs in the state dir — not data dir — because the XDG spec
// explicitly lists "actions history (logs, history, recently used files, ...)"
// as XDG_STATE_HOME content.
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

// DefaultAuditLogPath returns the XDG-derived audit log path.
// It is the fallback when no override is set in TOML.
func DefaultAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit.jsonl")
}

// DefaultClaudeAuditLogPath returns the XDG-derived audit log path for Claude hooks.
func DefaultClaudeAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit-claude.jsonl")
}

// DefaultCursorAuditLogPath returns the XDG-derived audit log path for Cursor hooks.
func DefaultCursorAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit-cursor.jsonl")
}

// DefaultCodexAuditLogPath returns the XDG-derived audit log path for Codex hooks.
func DefaultCodexAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit-codex.jsonl")
}

// DefaultGeminiAuditLogPath returns the XDG-derived audit log path for Gemini hooks.
func DefaultGeminiAuditLogPath() string {
	return filepath.Join(DefaultStateDir(), "audit-gemini.jsonl")
}

// ProfilesConfigPath returns the path to profiles.toml in the XDG config directory.
func ProfilesConfigPath() string {
	return filepath.Join(DefaultConfigDir(), "profiles.toml")
}

// RuntimeDir returns the XDG_RUNTIME_DIR for agent-gate.
//
// Resolution:
//
//	$XDG_RUNTIME_DIR/agent-gate   (if $XDG_RUNTIME_DIR is set — tmpfs on Linux/systemd)
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

// SessionRuntimeDir returns the runtime directory for a specific wrapper session.
// This is where the fake HOME lives for that claude process.
func SessionRuntimeDir(wrapperID string) string {
	return filepath.Join(RuntimeDir(), "sessions", wrapperID)
}

// FakeHomeDir returns the fake HOME directory path for a wrapper session.
func FakeHomeDir(wrapperID string) string {
	return filepath.Join(SessionRuntimeDir(wrapperID), "home")
}

// FakeClaudeDir returns the fake ~/.claude directory path for a wrapper session.
func FakeClaudeDir(wrapperID string) string {
	return filepath.Join(FakeHomeDir(wrapperID), ".claude")
}

// FakeSettingsPath returns the path to settings.json in the fake home.
func FakeSettingsPath(wrapperID string) string {
	return filepath.Join(FakeClaudeDir(wrapperID), "settings.json")
}

// EnsureRuntimeDir creates the agent-gate runtime directory with correct permissions.
// XDG spec requires 0700 for XDG_RUNTIME_DIR contents.
func EnsureRuntimeDir() error {
	dir := RuntimeDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("failed to create runtime dir %s: %w", dir, err)
	}
	return nil
}
