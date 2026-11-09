package hotkv

import (
	"errors"
	"math"
	"sort"
	"strings"
)

const unlimitedReadBytes = math.MaxInt64

// ErrReadOverflow reports a result that exceeds its caller's byte budget.
var ErrReadOverflow = errors.New("cache result exceeds read byte budget")

// ErrInvalidReadBudget reports a negative budget or entry charge.
var ErrInvalidReadBudget = errors.New("cache read budget and entry charges must be non-negative")

// EntrySizer computes a result charge from metadata and the requested value size.
// Metadata.Value is nil. A nil sizer charges namespace, key, and requested value
// bytes. Callers can supply transport accounting without cloning values first.
// The callback runs under the cache lock and must not call Store methods.
type EntrySizer func(metadata Entry, valueBytes int) (int64, error)

// GetBounded returns an owned copy after checking its complete result charge.
func (s *Store) GetBounded(
	namespace string,
	key string,
	includeValue bool,
	maxBytes int64,
	size EntrySizer,
) (Entry, bool, error) {
	return s.readBounded(namespace, key, includeValue, maxBytes, size, false)
}

// GetDeleteBounded removes an entry only after its result fits the byte budget.
func (s *Store) GetDeleteBounded(
	namespace string,
	key string,
	includeValue bool,
	maxBytes int64,
	size EntrySizer,
) (Entry, bool, error) {
	return s.readBounded(namespace, key, includeValue, maxBytes, size, true)
}

func (s *Store) readBounded(
	namespace string,
	key string,
	includeValue bool,
	maxBytes int64,
	size EntrySizer,
	remove bool,
) (Entry, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return emptyEntry(), false, err
	}

	if maxBytes < 0 {
		return emptyEntry(), false, ErrInvalidReadBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, found := s.entryLocked(storedKey, s.now())
	if !found {
		return emptyEntry(), false, nil
	}

	if _, err := chargeRead(storedKey, current, includeValue, maxBytes, size); err != nil {
		return emptyEntry(), false, err
	}

	out := exportEntry(storedKey, current, includeValue)
	if remove {
		s.removeLocked(storedKey)
	}

	return out, true, nil
}

// ListBounded checks every selected entry before cloning any result values.
// It returns ErrReadOverflow without partial results when the budget is too small.
func (s *Store) ListBounded(
	namespace string,
	prefix string,
	limit int,
	includeValues bool,
	maxBytes int64,
	size EntrySizer,
) ([]Entry, error) {
	if _, err := validateNamespace(namespace); err != nil {
		return nil, err
	}

	if strings.ContainsRune(prefix, '\x00') {
		return nil, ErrInvalidKey
	}

	if maxBytes < 0 {
		return nil, ErrInvalidReadBudget
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(s.now())
	keys := make([]entryKey, 0)

	for key := range s.entries {
		if key.namespace == namespace && strings.HasPrefix(key.key, prefix) {
			keys = append(keys, key)
		}
	}

	sort.Slice(keys, func(i, j int) bool { return keys[i].key < keys[j].key })
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}

	remaining := maxBytes

	for _, key := range keys {
		charge, err := chargeRead(key, s.entries[key], includeValues, remaining, size)
		if err != nil {
			return nil, err
		}

		remaining -= charge
	}

	out := make([]Entry, 0, len(keys))
	for _, key := range keys {
		out = append(out, exportEntry(key, s.entries[key], includeValues))
	}

	return out, nil
}

func chargeRead(
	key entryKey,
	current *entry,
	includeValue bool,
	remaining int64,
	size EntrySizer,
) (int64, error) {
	valueBytes := 0
	if includeValue {
		valueBytes = len(current.value)
	}

	charge := int64(len(key.namespace)) + int64(len(key.key)) + int64(valueBytes)
	if size != nil {
		var err error
		charge, err = size(exportEntry(key, current, false), valueBytes)
		if err != nil {
			return 0, err
		}
	}

	if charge < 0 {
		return 0, ErrInvalidReadBudget
	}

	if charge > remaining {
		return 0, ErrReadOverflow
	}

	return charge, nil
}
