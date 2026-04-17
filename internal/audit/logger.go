package audit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/gklog"
)

// Logger wraps slog and writes structured JSONL entries to the audit log file.
// Every hook invocation produces at least one log entry.
type Logger struct {
	inner  *slog.Logger
	closer io.Closer
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

	jsonMinLevel := cfg.Log.Level
	if jsonMinLevel == "" {
		jsonMinLevel = "info"
	}

	inner, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   path,
		Rotation:      gklog.RotationConfig{MaxSizeMB: 5, MaxBackups: 0, MaxAgeDays: 0},
		DisableStdout: true,
		JSONMinLevel:  jsonMinLevel,
	})
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	return &Logger{
		inner:  inner,
		closer: closer,
	}, nil
}

// Close flushes and closes the underlying rotating log writer.
func (l *Logger) Close() error {
	if l.closer == nil {
		return nil
	}
	return l.closer.Close()
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
