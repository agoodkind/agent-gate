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
	l.InfoContext(context.Background(), msg, attrs...)
}

// InfoContext writes an INFO-level audit entry bound to ctx for slog handlers.
func (l *Logger) InfoContext(ctx context.Context, msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(ctx, slog.LevelInfo, msg, attrs...)
}

// Debug writes a DEBUG-level entry (recorded only when level = "debug").
func (l *Logger) Debug(msg string, attrs ...slog.Attr) {
	l.DebugContext(context.Background(), msg, attrs...)
}

// DebugContext writes a DEBUG-level entry bound to ctx for slog handlers.
func (l *Logger) DebugContext(ctx context.Context, msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(ctx, slog.LevelDebug, msg, attrs...)
}

// Error writes an ERROR-level entry.
func (l *Logger) Error(msg string, attrs ...slog.Attr) {
	l.ErrorContext(context.Background(), msg, attrs...)
}

// ErrorContext writes an ERROR-level entry bound to ctx for slog handlers.
func (l *Logger) ErrorContext(ctx context.Context, msg string, attrs ...slog.Attr) {
	l.inner.LogAttrs(ctx, slog.LevelError, msg, attrs...)
}

// Loggers holds per-provider audit loggers.
type Loggers struct {
	BySystem map[string]*Logger
}

// NewLoggers opens all per-provider audit log files and returns a Loggers.
func NewLoggers(cfg *config.Config) (*Loggers, error) {
	level := cfg.Log.Level
	paths := map[string]string{
		"claude": cfg.ClaudeAuditLogPath(),
		"cursor": cfg.CursorAuditLogPath(),
		"codex":  cfg.CodexAuditLogPath(),
		"gemini": cfg.GeminiAuditLogPath(),
	}

	bySystem := make(map[string]*Logger, len(paths))
	for system, path := range paths {
		logger, err := newLogger(path, level)
		if err != nil {
			for _, existing := range bySystem {
				_ = existing.Close()
			}
			return nil, fmt.Errorf("open %s audit log: %w", system, err)
		}
		bySystem[system] = logger
	}

	return &Loggers{BySystem: bySystem}, nil
}

// For returns the logger for the given system string.
// Unknown systems fall back to the Claude logger.
func (l *Loggers) For(system string) *Logger {
	if logger, ok := l.BySystem[system]; ok {
		return logger
	}
	return l.BySystem["claude"]
}

// Close closes both underlying loggers.
func (l *Loggers) Close() error {
	for _, logger := range l.BySystem {
		if err := logger.Close(); err != nil {
			return err
		}
	}
	return nil
}
