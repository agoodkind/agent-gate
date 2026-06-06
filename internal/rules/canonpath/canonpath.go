// Package canonpath resolves filesystem paths to their canonical real form so
// that symlink aliases cannot split a cache key or evade a path allowlist. It
// is used by the exec validator condition, where the canonical real path is the
// source of truth for both the result cache key and the data handed to the
// external script.
package canonpath

import (
	"path/filepath"
	"sync"
	"time"
)

// Result holds both views of a resolved path. Raw is the absolute, cleaned
// input before symlink resolution; Canonical is the real path after
// [filepath.EvalSymlinks]. IsCanonical is false when the path could not be
// resolved (for example it does not exist), in which case Canonical falls back
// to Raw so callers always have a usable, deterministic value.
type Result struct {
	Raw         string
	Canonical   string
	IsCanonical bool
}

// Resolve makes path absolute (joining against cwd when path is relative),
// cleans it, then resolves symlinks. A path that cannot be resolved (most often
// because it does not exist) degrades gracefully to the absolute cleaned form
// with IsCanonical false rather than returning an error, so an ephemeral or
// already-removed target never breaks the caller.
func Resolve(cwd string, path string) Result {
	if path == "" {
		return Result{Raw: "", Canonical: "", IsCanonical: false}
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(cwd, abs)
	}
	abs = filepath.Clean(abs)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Result{Raw: abs, Canonical: abs, IsCanonical: false}
	}
	return Result{Raw: abs, Canonical: resolved, IsCanonical: true}
}

type cacheEntry struct {
	result   Result
	expireAt time.Time
}

// Cache memoizes Resolve for a short window. [filepath.EvalSymlinks] issues one
// lstat per path component, and the same working directories repeat heavily
// across events, so a brief TTL avoids redundant syscalls without holding a
// stale realpath long enough to matter. The zero value is not usable; call
// NewCache.
type Cache struct {
	ttl     time.Duration
	mu      sync.Mutex
	entries map[string]cacheEntry
	now     func() time.Time
}

// NewCache returns a Cache that memoizes results for ttl. A non-positive ttl
// disables memoization, so every call resolves freshly.
func NewCache(ttl time.Duration) *Cache {
	return &Cache{
		ttl:     ttl,
		mu:      sync.Mutex{},
		entries: make(map[string]cacheEntry),
		now:     time.Now,
	}
}

// Resolve returns the canonical result for (cwd, path), serving a memoized value
// when one is live and resolving freshly otherwise.
func (c *Cache) Resolve(cwd string, path string) Result {
	if c == nil || c.ttl <= 0 {
		return Resolve(cwd, path)
	}
	key := cwd + "\x00" + path
	now := c.now()

	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && now.Before(entry.expireAt) {
		c.mu.Unlock()
		return entry.result
	}
	c.mu.Unlock()

	result := Resolve(cwd, path)

	c.mu.Lock()
	c.entries[key] = cacheEntry{result: result, expireAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return result
}
