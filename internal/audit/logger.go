package audit

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/version"
	"goodkind.io/gklog"
)

// Logger wraps slog and writes structured JSONL entries to an audit log file.
type Logger struct {
	inner  *slog.Logger
	closer io.Closer
}

// New opens (or creates) the audit log file at cfg.AuditLogPath() and returns a Logger.
func New(cfg *config.Config) (*Logger, error) {
	return newLogger(cfg.AuditLogPath(), cfg.Log.Level)
}

// newLogger opens (or creates) the audit log at path with the given level.
func newLogger(path, level string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create audit log dir %s: %w", filepath.Dir(path), err)
	}

	minLevel := level
	if minLevel == "" {
		minLevel = "info"
	}

	inner, closer, err := gklog.New(gklog.Config{
		JSONLogFile:   path,
		Rotation:      gklog.RotationConfig{MaxSizeMB: 5, MaxBackups: 0, MaxAgeDays: 0},
		DisableStdout: true,
		JSONMinLevel:  minLevel,
	})
	if err != nil {
		return nil, fmt.Errorf("open audit log %s: %w", path, err)
	}

	inner = inner.With(
		slog.String("commit", version.Commit),
		slog.String("version", version.Version),
		slog.String("buildHash", version.BuildHash()),
		slog.String("dirty", version.Dirty),
	)

	return &Logger{inner: inner, closer: closer}, nil
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

// Loggers holds separate audit loggers for Claude and Cursor hooks.
// All Claude hook events write to the Claude log; all Cursor events write to the Cursor log.
type Loggers struct {
	Claude *Logger
	Cursor *Logger
}

// NewLoggers opens both per-system audit log files and returns a Loggers.
func NewLoggers(cfg *config.Config) (*Loggers, error) {
	level := cfg.Log.Level

	claude, err := newLogger(cfg.ClaudeAuditLogPath(), level)
	if err != nil {
		return nil, fmt.Errorf("open claude audit log: %w", err)
	}

	cursor, err := newLogger(cfg.CursorAuditLogPath(), level)
	if err != nil {
		_ = claude.Close()
		return nil, fmt.Errorf("open cursor audit log: %w", err)
	}

	return &Loggers{Claude: claude, Cursor: cursor}, nil
}

// For returns the logger for the given system string ("claude", "cursor", or any other).
// Unknown systems fall back to the Claude logger.
func (l *Loggers) For(system string) *Logger {
	if system == "cursor" {
		return l.Cursor
	}
	return l.Claude
}

// Close closes both underlying loggers.
func (l *Loggers) Close() error {
	err1 := l.Claude.Close()
	err2 := l.Cursor.Close()
	if err1 != nil {
		return err1
	}
	return err2
}
