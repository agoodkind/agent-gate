package audit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
	expirable "github.com/hashicorp/golang-lru/v2/expirable"
)

// sessionLogCacheSize is the maximum number of open file handles kept in
// the LRU. Eviction closes the file before it leaves the cache.
const sessionLogCacheSize = 64

// dedupCacheSize is how many recent entry fingerprints we remember to
// drop duplicate fires. Editors sometimes merge hook entries from
// multiple config files (settings.json, hooks/*.json, etc.) and end up
// invoking agent-gate twice for one tool call. Without dedup we would
// double-log every such event.
const dedupCacheSize = 4096

// dedupTTL is how long a fingerprint blocks duplicate writes. Long
// enough to cover the inter-arrival gap of merged-config double fires
// (microseconds) and short enough that legitimate identical events
// hours apart are not silently dropped.
const dedupTTL = 30 * time.Second

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
	dedup    *expirable.LRU[string, struct{}]

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
	sl.dedup = expirable.NewLRU[string, struct{}](dedupCacheSize, nil, dedupTTL)

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

	// Drop duplicate fires that share the same payload within dedupTTL.
	// Editors that merge hook entries from multiple config files end up
	// invoking agent-gate twice for one tool call; without this, every
	// such event would be written twice.
	fingerprint := dedupFingerprint(key, level, msg, attrs)
	if _, seen := sl.dedup.Get(fingerprint); seen {
		sl.log.Debug("session log dedup drop",
			"system", key.system,
			"session_id", key.sessionID,
			"event", key.eventName,
			"msg", msg,
		)
		return
	}
	sl.dedup.Add(fingerprint, struct{}{})

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

// dedupFingerprint produces a stable hash for an entry so that duplicate
// fires (same destination, same level, same message, same payload) collapse
// to a single write. The "time" attribute is excluded because it differs
// on each invocation. Other attributes are serialised in sorted order so
// map iteration randomness does not change the hash.
func dedupFingerprint(key sessionKey, level, msg string, attrs map[string]any) string {
	stable := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if k == "time" {
			continue
		}
		stable[k] = v
	}
	keys := make([]string, 0, len(stable))
	for k := range stable {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	_, _ = h.Write([]byte(key.system))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key.sessionID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(key.eventName))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(level))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(msg))
	_, _ = h.Write([]byte{0})
	for _, k := range keys {
		_, _ = h.Write([]byte(k))
		_, _ = h.Write([]byte{'='})
		if b, err := json.Marshal(stable[k]); err == nil {
			_, _ = h.Write(b)
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
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
