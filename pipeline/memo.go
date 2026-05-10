package pipeline

import (
	"context"
	"time"
)

// Cache hands out a Handle scoped to a lifetime and TTL.
type Cache interface {
	For(ctx context.Context, lifetime MemoLifetime, ttl time.Duration) Handle
}

// Handle reads and writes byte-blob memoizations keyed by string.
type Handle interface {
	Get(key string) ([]byte, bool)
	Put(key string, value []byte)
}

// EventCache is a one-shot per-Run memo backed by an unsynchronized map.
type EventCache struct {
	entries map[string][]byte
}

// NewEventCache constructs an empty EventCache ready for one Run.
func NewEventCache() *EventCache {
	return &EventCache{entries: make(map[string][]byte)}
}

// For returns the EventCache itself; lifetime and ttl are ignored at this landing.
func (c *EventCache) For(_ context.Context, _ MemoLifetime, _ time.Duration) Handle {
	return c
}

// Get returns the stored value and whether the key was present.
func (c *EventCache) Get(key string) ([]byte, bool) {
	value, ok := c.entries[key]
	return value, ok
}

// Put stores the value under the given key, overwriting any prior entry.
func (c *EventCache) Put(key string, value []byte) {
	c.entries[key] = value
}
