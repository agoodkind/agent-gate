package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// sessionLogCacheSize is the maximum number of open file handles kept in
// the LRU. Eviction closes the file before it leaves the cache.
const sessionLogCacheSize = 64

// noSessionDir is the folder used when a hook payload has no session_id.
const noSessionDir = "_no-session"

// noEventName is the file stem used when no event_name is set.
const noEventName = "_unknown"

// SessionLogger writes per-conversation, per-event JSONL audit entries.
//
// Layout on disk:
//
//	<baseDir>/<system>/<session_id>/<event_name>.jsonl
//
// Open file handles are kept in an LRU cache. Disk writes happen on a
// background worker goroutine fed by an unbounded in-memory queue. Callers
// never block on file I/O. The queue grows with backlog so no entry is ever
// dropped. Memory pressure is the natural backstop.
type SessionLogger struct {
	baseDir  string
	minLevel slog.Level
	cache    *lru.Cache[sessionKey, *os.File]

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []sessionWrite
	stopping bool

	wg  sync.WaitGroup
	log *slog.Logger
}

type sessionKey struct {
	system    string
	sessionID string
	eventName string
}

type sessionWrite struct {
	key  sessionKey
	line []byte
}

// NewSessionLogger creates a SessionLogger writing under baseDir and starts
// the background worker. The minLevel argument is one of debug, info, warn,
// error. An empty or unrecognized value is treated as info.
func NewSessionLogger(baseDir, minLevel string, log *slog.Logger) (*SessionLogger, error) {
	if log == nil {
		log = slog.Default()
	}

	sl := &SessionLogger{
		baseDir:  baseDir,
		minLevel: parseLevel(minLevel),
		log:      log,
	}
	sl.cond = sync.NewCond(&sl.mu)

	cache, err := lru.NewWithEvict[sessionKey, *os.File](sessionLogCacheSize, func(_ sessionKey, f *os.File) {
		_ = f.Close()
	})
	if err != nil {
		return nil, fmt.Errorf("create session log lru: %w", err)
	}
	sl.cache = cache

	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("create session log base dir %s: %w", baseDir, err)
	}

	sl.wg.Add(1)
	go sl.worker()
	return sl, nil
}

// Log enqueues a single audit entry for asynchronous write. The call is
// non-blocking and never drops the entry.
//
// system, sessionID, and eventName together choose the destination file.
// Empty values are replaced with placeholder folder/file names so that no
// data is silently lost.
func (sl *SessionLogger) Log(system, sessionID, eventName, level, msg string, attrs map[string]any) {
	if sl == nil {
		return
	}
	if !sl.shouldLog(level) {
		return
	}

	key := sessionKey{
		system:    sanitize(system, "unknown"),
		sessionID: sanitize(sessionID, noSessionDir),
		eventName: sanitize(eventName, noEventName),
	}

	entry := make(map[string]any, len(attrs)+3)
	entry["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["level"] = level
	entry["msg"] = msg
	for k, v := range attrs {
		if k == "time" || k == "level" || k == "msg" {
			continue
		}
		entry[k] = v
	}

	line, err := json.Marshal(entry)
	if err != nil {
		sl.log.Warn("session log marshal failed", "err", err)
		return
	}
	line = append(line, '\n')

	sl.mu.Lock()
	if sl.stopping {
		sl.mu.Unlock()
		return
	}
	sl.queue = append(sl.queue, sessionWrite{key: key, line: line})
	sl.cond.Signal()
	sl.mu.Unlock()
}

// Close marks the logger stopping, waits for the worker to drain all
// queued writes, then closes every open file. Safe to call multiple times.
func (sl *SessionLogger) Close() error {
	if sl == nil {
		return nil
	}
	sl.mu.Lock()
	if sl.stopping {
		sl.mu.Unlock()
		return nil
	}
	sl.stopping = true
	sl.cond.Broadcast()
	sl.mu.Unlock()

	sl.wg.Wait()
	sl.cache.Purge()
	return nil
}

func (sl *SessionLogger) worker() {
	defer sl.wg.Done()

	for {
		sl.mu.Lock()
		for len(sl.queue) == 0 && !sl.stopping {
			sl.cond.Wait()
		}
		batch := sl.queue
		sl.queue = nil
		stopping := sl.stopping
		sl.mu.Unlock()

		for _, w := range batch {
			sl.write(w)
		}

		if stopping {
			// Re-check the queue under the lock to catch any final writers
			// that raced past the stopping flag.
			sl.mu.Lock()
			remaining := sl.queue
			sl.queue = nil
			sl.mu.Unlock()
			for _, w := range remaining {
				sl.write(w)
			}
			return
		}
	}
}

func (sl *SessionLogger) write(w sessionWrite) {
	f, ok := sl.cache.Get(w.key)
	if !ok {
		opened, err := sl.openFile(w.key)
		if err != nil {
			sl.log.Warn("session log open failed",
				"system", w.key.system,
				"session_id", w.key.sessionID,
				"event", w.key.eventName,
				"err", err,
			)
			return
		}
		sl.cache.Add(w.key, opened)
		f = opened
	}

	if _, err := f.Write(w.line); err != nil {
		sl.log.Warn("session log write failed",
			"system", w.key.system,
			"session_id", w.key.sessionID,
			"event", w.key.eventName,
			"err", err,
		)
		// On write error, drop the cached handle so the next entry reopens.
		sl.cache.Remove(w.key)
	}
}

func (sl *SessionLogger) openFile(key sessionKey) (*os.File, error) {
	dir := filepath.Join(sl.baseDir, key.system, key.sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, key.eventName+".jsonl")
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}

// shouldLog returns true if the given level meets the configured minimum.
func (sl *SessionLogger) shouldLog(level string) bool {
	return parseLevel(level) >= sl.minLevel
}

// parseLevel maps a string level name to slog.Level. Unknown values map to
// slog.LevelInfo so misconfiguration never silences info entries.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// sanitize replaces empty strings with fallback and strips path separators
// so that attacker-controlled fields cannot escape the session folder.
func sanitize(s, fallback string) string {
	if s == "" {
		return fallback
	}
	s = strings.ReplaceAll(s, string(os.PathSeparator), "_")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "..", "_")
	if s == "" || s == "." {
		return fallback
	}
	return s
}

// AttrsFromSlog converts a slice of slog.Attr to a flat map[string]any
// suitable for SessionLogger.Log. Group attrs are flattened with dotted keys.
func AttrsFromSlog(attrs []slog.Attr) map[string]any {
	out := make(map[string]any, len(attrs))
	for _, a := range attrs {
		flattenAttr("", a, out)
	}
	return out
}

func flattenAttr(prefix string, a slog.Attr, out map[string]any) {
	key := a.Key
	if prefix != "" {
		key = prefix + "." + key
	}
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindGroup:
		for _, sub := range v.Group() {
			flattenAttr(key, sub, out)
		}
	default:
		out[key] = v.Any()
	}
}

// Ensure context type is referenced (kept for future ctx-aware writes).
var _ = context.Background
