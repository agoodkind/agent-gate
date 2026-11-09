package hotkv

import (
	"errors"
	"sort"
	"strings"
	"time"
)

// ErrCapacity reports that one entry cannot fit the aggregate byte limit.
var ErrCapacity = errors.New("entry exceeds cache byte capacity")

func (s *Store) fitsLocked(key entryKey, valueBytes int) bool {
	remaining := s.options.MaxBytes - len(key.namespace) - len(key.key)
	return remaining >= 0 && valueBytes <= remaining
}

// prepareWriteLocked reserves capacity before the caller copies retained data.
// The replacement key cannot evict itself, even when update times match.
func (s *Store) prepareWriteLocked(key entryKey, valueBytes int, now time.Time) bool {
	if !s.fitsLocked(key, valueBytes) {
		return false
	}

	s.pruneExpiredLocked(now)
	oldCharge := 0
	newEntries := 1
	if current := s.entries[key]; current != nil {
		oldCharge = len(key.namespace) + len(key.key) + len(current.value)
		newEntries = 0
	}

	charge := len(key.namespace) + len(key.key) + valueBytes
	if len(s.entries)+newEntries <= s.options.MaxEntries &&
		s.usedBytes-oldCharge <= s.options.MaxBytes-charge {
		return true
	}

	for _, candidate := range s.evictionKeysLocked(key) {
		s.removeLocked(candidate)
		if len(s.entries)+newEntries <= s.options.MaxEntries &&
			s.usedBytes-oldCharge <= s.options.MaxBytes-charge {
			return true
		}
	}

	return false
}

func (s *Store) evictionKeysLocked(excluded entryKey) []entryKey {
	keys := make([]entryKey, 0, len(s.entries))
	for key := range s.entries {
		if key != excluded {
			keys = append(keys, key)
		}
	}

	sort.Slice(
		keys,
		func(i, j int) bool {
			left := s.entries[keys[i]]
			right := s.entries[keys[j]]
			if !left.updatedAt.Equal(right.updatedAt) {
				return left.updatedAt.Before(right.updatedAt)
			}

			if keys[i].namespace != keys[j].namespace {
				return keys[i].namespace < keys[j].namespace
			}

			return keys[i].key < keys[j].key
		},
	)

	return keys
}

func (s *Store) writeLocked(key entryKey, value []byte, now time.Time, ttl time.Duration) *entry {
	current := s.entries[key]
	if current == nil {
		key = entryKey{namespace: strings.Clone(key.namespace), key: strings.Clone(key.key)}
		current = &entry{
			value:       nil,
			order:       ObservationOrder{Generation: 0, ReceiptID: 0},
			unavailable: false,
			version:     0,
			createdAt:   now,
			updatedAt:   now,
			expiresAt:   time.Time{},
		}
		s.entries[key] = current
		s.usedBytes += len(key.namespace) + len(key.key)
	}

	s.usedBytes -= len(current.value)
	current.value = nil
	s.usedBytes += len(value)
	current.value = make([]byte, len(value))
	copy(current.value, value)
	current.version++
	current.updatedAt = now
	current.expiresAt = time.Time{}
	if ttl > 0 {
		current.expiresAt = now.Add(ttl)
	}

	return current
}

func (s *Store) removeLocked(key entryKey) {
	current := s.entries[key]
	if current == nil {
		return
	}

	if current.order.ReceiptID > 0 {
		s.discardedBefore = s.highestRegistered
	}

	s.usedBytes -= len(key.namespace) + len(key.key) + len(current.value)
	delete(s.entries, key)
}
