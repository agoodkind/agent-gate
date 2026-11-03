package audit

import (
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
)

type dedupEntry struct {
	fingerprint string
	expires     time.Time
	previous    *dedupEntry
	next        *dedupEntry
}

// auditDedupCache requires the owning logger's mutex.
type auditDedupCache struct {
	entries *simplelru.LRU[string, *dedupEntry]
	first   *dedupEntry
	last    *dedupEntry
	ttl     time.Duration
}

func newAuditDedupCache(size int, ttl time.Duration) (*auditDedupCache, error) {
	cache := &auditDedupCache{entries: nil, first: nil, last: nil, ttl: ttl}
	entries, err := simplelru.NewLRU[string, *dedupEntry](size, func(_ string, entry *dedupEntry) {
		cache.removeExpiration(entry)
	})
	if err != nil {
		return nil, err
	}
	cache.entries = entries
	return cache, nil
}

func (cache *auditDedupCache) expire(now time.Time) {
	for cache.first != nil {
		entry := cache.first
		if !now.After(entry.expires) {
			return
		}
		cache.entries.Remove(entry.fingerprint)
	}
}

func (cache *auditDedupCache) seen(fingerprint string, now time.Time) bool {
	cache.expire(now)
	_, exists := cache.entries.Get(fingerprint)
	return exists
}

func (cache *auditDedupCache) remember(fingerprint string, now time.Time) {
	cache.expire(now)
	entry, exists := cache.entries.Peek(fingerprint)
	if exists {
		entry.expires = now.Add(cache.ttl)
		cache.removeExpiration(entry)
	} else {
		entry = &dedupEntry{fingerprint: fingerprint, expires: now.Add(cache.ttl), previous: nil, next: nil}
	}
	entry.previous = cache.last
	if cache.last == nil {
		cache.first = entry
	} else {
		cache.last.next = entry
	}
	cache.last = entry
	cache.entries.Add(fingerprint, entry)
}

func (cache *auditDedupCache) removeExpiration(entry *dedupEntry) {
	if entry.previous == nil {
		cache.first = entry.next
	} else {
		entry.previous.next = entry.next
	}
	if entry.next == nil {
		cache.last = entry.previous
	} else {
		entry.next.previous = entry.previous
	}
	entry.previous = nil
	entry.next = nil
}

func (cache *auditDedupCache) purge() {
	cache.entries.Purge()
}
