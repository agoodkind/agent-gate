package audit

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/agent-gate/internal/config"
)

// Logger wraps slog and writes structured JSONL entries to the audit log file.
// Every hook invocation produces at least one log entry.
type Logger struct {
	inner *slog.Logger
	file  *os.File
}

// New opens (or creates) the audit log file and returns a Logger.
// The log path is taken from cfg.Log.Path; if empty, the XDG default is used.
func New(cfg *config.Config) (*Logger, error) {
	// AuditLogPath applies the full resolution chain:
	// TOML [paths].audit_log > $XDG_STATE_HOME > ~/.local/state/agent-gate/audit.jsonl
	path := cfg.AuditLogPath()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit log dir %s: %w", filepath.Dir(path), err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	level := parseLevel(cfg.Log.Level)
	handler := slog.NewJSONHandler(f, &slog.HandlerOptions{Level: level})

	return &Logger{
		inner: slog.New(handler),
		file:  f,
	}, nil
}

// Close flushes and closes the underlying log file.
func (l *Logger) Close() error {
	return l.file.Close()
}

// Info writes an INFO-level audit entry with arbitrary structured attributes.
func (l *Logger) Info(msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(context.TODO(), slog.LevelInfo, msg, attrs...)
}

// Debug writes a DEBUG-level entry (recorded only when level = "debug").
func (l *Logger) Debug(msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(context.TODO(), slog.LevelDebug, msg, attrs...)
}

// Error writes an ERROR-level entry.
func (l *Logger) Error(msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(context.TODO(), slog.LevelError, msg, attrs...)
}

// parseLevel converts a config level string to slog.Level.
// Unknown values default to INFO.
func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
