// Package hotkv provides a process-local Redis-shaped key-value cache with TTLs.
package hotkv

import (
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxEntries is the default maximum number of hot cache entries.
	DefaultMaxEntries = 4096
	// DefaultMaxValueBytes is the default maximum value size for one entry.
	DefaultMaxValueBytes = 1 << 20
	// DefaultPruneInterval is the default interval for periodic expired-entry pruning.
	DefaultPruneInterval = 30 * time.Second

	// MaxNamespaceBytes is the maximum namespace length in bytes.
	MaxNamespaceBytes = 128
	// MaxKeyBytes is the maximum key length in bytes.
	MaxKeyBytes = 2048
)

var (
	// ErrInvalidNamespace reports an empty, oversized, or NUL-containing namespace.
	ErrInvalidNamespace = errors.New("invalid namespace")
	// ErrInvalidKey reports an empty, oversized, or NUL-containing key.
	ErrInvalidKey = errors.New("invalid key")
	// ErrInvalidSetMode reports an unsupported conditional mode for Set.
	ErrInvalidSetMode = errors.New("invalid set mode")
	// ErrValueTooLarge reports a value that exceeds the configured value limit.
	ErrValueTooLarge = errors.New("value exceeds max size")
)

// Options configures Store memory bounds and expiry pruning.
type Options struct {
	MaxEntries    int
	MaxValueBytes int
	PruneInterval time.Duration
}

// SetMode selects the Redis-style conditional mode for Store.Set.
type SetMode string

const (
	// SetModeAny stores the value unconditionally.
	SetModeAny SetMode = ""
	// SetModeNX stores only when the key does not already exist.
	SetModeNX SetMode = "NX"
	// SetModeXX stores only when the key already exists.
	SetModeXX SetMode = "XX"
)

// SetOptions configures Store.Set conditional mode and expiry.
type SetOptions struct {
	Mode SetMode
	TTL  time.Duration
}

// Entry is a point-in-time copy of one hot cache entry.
type Entry struct {
	Namespace string
	Key       string
	Value     []byte
	Version   uint64
	CreatedAt time.Time
	UpdatedAt time.Time
	ExpiresAt time.Time
}

// Store is a concurrency-safe in-memory TTL key-value cache.
type Store struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[entryKey]*entry
	options Options
	closed  bool

	stopPrune chan struct{}
	pruneDone chan struct{}
}

type entryKey struct {
	namespace string
	key       string
}

type entry struct {
	value     []byte
	version   uint64
	createdAt time.Time
	updatedAt time.Time
	expiresAt time.Time
}

// New returns a Store using normalized options.
func New(options Options) *Store {
	options = normalizeOptions(options)
	store := &Store{
		mu:        sync.Mutex{},
		now:       time.Now,
		entries:   make(map[entryKey]*entry),
		options:   options,
		closed:    false,
		stopPrune: make(chan struct{}),
		pruneDone: make(chan struct{}),
	}
	if options.PruneInterval > 0 {
		store.launchPruneLoop(options.PruneInterval, store.stopPrune, store.pruneDone)
	} else {
		close(store.pruneDone)
	}
	return store
}

func normalizeOptions(options Options) Options {
	if options.MaxEntries <= 0 {
		options.MaxEntries = DefaultMaxEntries
	}
	if options.MaxValueBytes <= 0 {
		options.MaxValueBytes = DefaultMaxValueBytes
	}
	if options.PruneInterval < 0 {
		options.PruneInterval = 0
	}
	return options
}

// Close stops the optional pruning goroutine.
func (s *Store) Close() {
	if s == nil {
		return
	}
	var (
		stop         chan struct{}
		done         chan struct{}
		pruningAlive bool
	)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	stop = s.stopPrune
	done = s.pruneDone
	pruningAlive = s.options.PruneInterval > 0
	s.mu.Unlock()
	if !pruningAlive {
		return
	}
	close(stop)
	<-done
}

// Configure updates memory bounds and prunes entries over the new limit.
func (s *Store) Configure(options Options) {
	if s == nil {
		return
	}
	options = normalizeOptions(options)
	var (
		oldStop       chan struct{}
		oldDone       chan struct{}
		newStop       chan struct{}
		newDone       chan struct{}
		startInterval time.Duration
	)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.options.MaxEntries = options.MaxEntries
	s.options.MaxValueBytes = options.MaxValueBytes
	s.pruneOversizedLocked()
	if s.options.PruneInterval != options.PruneInterval {
		if s.options.PruneInterval > 0 {
			oldStop = s.stopPrune
			oldDone = s.pruneDone
		}
		s.options.PruneInterval = options.PruneInterval
		newStop = make(chan struct{})
		newDone = make(chan struct{})
		s.stopPrune = newStop
		s.pruneDone = newDone
		if options.PruneInterval > 0 {
			startInterval = options.PruneInterval
			s.launchPruneLoop(startInterval, newStop, newDone)
		} else {
			close(s.pruneDone)
		}
	}
	s.enforceMaxEntriesLocked(s.now())
	s.mu.Unlock()
	if oldStop != nil {
		close(oldStop)
		<-oldDone
	}
}

// Get returns a copy of one entry when it exists and has not expired.
func (s *Store) Get(namespace string, key string) (Entry, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return emptyEntry(), false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	found, ok := s.entryLocked(storedKey, now)
	if !ok {
		return emptyEntry(), false, nil
	}
	return exportEntry(storedKey, found), true, nil
}

// Set stores a value with optional NX, XX, and TTL semantics.
func (s *Store) Set(namespace string, key string, value []byte, options SetOptions) (Entry, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return emptyEntry(), false, err
	}
	if err := s.validateValue(value); err != nil {
		return emptyEntry(), false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	current, exists := s.entryLocked(storedKey, now)
	switch options.Mode {
	case SetModeNX:
		if exists {
			return emptyEntry(), false, nil
		}
	case SetModeXX:
		if !exists {
			return emptyEntry(), false, nil
		}
	case SetModeAny:
	default:
		return emptyEntry(), false, ErrInvalidSetMode
	}

	expiresAt := time.Time{}
	if options.TTL > 0 {
		expiresAt = now.Add(options.TTL)
	}
	if !exists {
		current = &entry{
			value:     nil,
			version:   0,
			createdAt: now,
			updatedAt: time.Time{},
			expiresAt: time.Time{},
		}
		s.entries[storedKey] = current
	}
	current.value = append([]byte(nil), value...)
	current.version++
	current.updatedAt = now
	current.expiresAt = expiresAt
	s.enforceMaxEntriesLocked(now)
	return exportEntry(storedKey, current), true, nil
}

// Delete removes one entry and reports whether it existed.
func (s *Store) Delete(namespace string, key string) (bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if _, ok := s.entryLocked(storedKey, now); !ok {
		return false, nil
	}
	delete(s.entries, storedKey)
	return true, nil
}

// Exists reports whether an entry exists and has not expired.
func (s *Store) Exists(namespace string, key string) (bool, error) {
	_, found, err := s.Get(namespace, key)
	return found, err
}

// GetDelete returns one entry and removes it when present.
func (s *Store) GetDelete(namespace string, key string) (Entry, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return emptyEntry(), false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	found, ok := s.entryLocked(storedKey, now)
	if !ok {
		return emptyEntry(), false, nil
	}
	out := exportEntry(storedKey, found)
	delete(s.entries, storedKey)
	return out, true, nil
}

// Expire updates one entry's expiry and treats non-positive TTL as delete.
func (s *Store) Expire(namespace string, key string, ttl time.Duration) (bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return false, err
	}
	if ttl <= 0 {
		return s.Delete(namespace, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	found, ok := s.entryLocked(storedKey, now)
	if !ok {
		return false, nil
	}
	found.version++
	found.updatedAt = now
	found.expiresAt = now.Add(ttl)
	return true, nil
}

// PTTL returns the remaining TTL, whether the key exists, and whether it expires.
func (s *Store) PTTL(namespace string, key string) (time.Duration, bool, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return 0, false, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	found, ok := s.entryLocked(storedKey, now)
	if !ok {
		return 0, false, false, nil
	}
	if found.expiresAt.IsZero() {
		return 0, true, false, nil
	}
	ttl := found.expiresAt.Sub(now)
	if ttl <= 0 {
		delete(s.entries, storedKey)
		return 0, false, false, nil
	}
	return ttl, true, true, nil
}

// List returns entries in namespace whose key has prefix, sorted by key.
func (s *Store) List(namespace string, prefix string, limit int, includeValues bool) ([]Entry, error) {
	if _, err := validateNamespace(namespace); err != nil {
		return nil, err
	}
	if strings.ContainsRune(prefix, '\x00') {
		return nil, ErrInvalidKey
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneExpiredLocked(now)
	keys := make([]entryKey, 0)
	for storedKey := range s.entries {
		if storedKey.namespace != namespace {
			continue
		}
		if prefix != "" && !strings.HasPrefix(storedKey.key, prefix) {
			continue
		}
		keys = append(keys, storedKey)
	}
	sort.Slice(keys, func(i int, j int) bool {
		return keys[i].key < keys[j].key
	})
	if limit > 0 && len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]Entry, 0, len(keys))
	for _, storedKey := range keys {
		current := s.entries[storedKey]
		item := exportEntry(storedKey, current)
		if !includeValues {
			item.Value = nil
		}
		out = append(out, item)
	}
	return out, nil
}

// PruneExpired removes currently expired entries.
func (s *Store) PruneExpired() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.pruneExpiredLocked(s.now())
	s.mu.Unlock()
}

func (s *Store) entryLocked(storedKey entryKey, now time.Time) (*entry, bool) {
	found, ok := s.entries[storedKey]
	if !ok {
		return nil, false
	}
	if isExpired(found, now) {
		delete(s.entries, storedKey)
		return nil, false
	}
	return found, true
}

func (s *Store) pruneExpiredLocked(now time.Time) {
	for storedKey, current := range s.entries {
		if isExpired(current, now) {
			delete(s.entries, storedKey)
		}
	}
}

func (s *Store) enforceMaxEntriesLocked(now time.Time) {
	s.pruneExpiredLocked(now)
	s.pruneOversizedLocked()
	maxEntries := s.options.MaxEntries
	if maxEntries <= 0 || len(s.entries) <= maxEntries {
		return
	}
	keys := make([]entryKey, 0, len(s.entries))
	for storedKey := range s.entries {
		keys = append(keys, storedKey)
	}
	sort.Slice(keys, func(i int, j int) bool {
		left := s.entries[keys[i]]
		right := s.entries[keys[j]]
		return left.updatedAt.Before(right.updatedAt)
	})
	for len(s.entries) > maxEntries && len(keys) > 0 {
		delete(s.entries, keys[0])
		keys = keys[1:]
	}
}

func (s *Store) pruneOversizedLocked() {
	maxValueBytes := s.options.MaxValueBytes
	if maxValueBytes <= 0 {
		return
	}
	for storedKey, current := range s.entries {
		if len(current.value) > maxValueBytes {
			delete(s.entries, storedKey)
		}
	}
}

func (s *Store) validateValue(value []byte) error {
	maxValueBytes := DefaultMaxValueBytes
	if s != nil && s.options.MaxValueBytes > 0 {
		maxValueBytes = s.options.MaxValueBytes
	}
	if len(value) > maxValueBytes {
		return ErrValueTooLarge
	}
	return nil
}

func (s *Store) launchPruneLoop(interval time.Duration, stop <-chan struct{}, done chan struct{}) {
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("hotkv prune loop panic", "err", recovered)
			}
		}()
		s.pruneLoop(interval, stop, done)
	}()
}

func (s *Store) pruneLoop(interval time.Duration, stop <-chan struct{}, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.PruneExpired()
		case <-stop:
			return
		}
	}
}

func isExpired(current *entry, now time.Time) bool {
	if current == nil || current.expiresAt.IsZero() {
		return false
	}
	return !current.expiresAt.After(now)
}

func exportEntry(storedKey entryKey, current *entry) Entry {
	if current == nil {
		return emptyEntry()
	}
	return Entry{
		Namespace: storedKey.namespace,
		Key:       storedKey.key,
		Value:     append([]byte(nil), current.value...),
		Version:   current.version,
		CreatedAt: current.createdAt,
		UpdatedAt: current.updatedAt,
		ExpiresAt: current.expiresAt,
	}
}

func emptyEntry() Entry {
	return Entry{
		Namespace: "",
		Key:       "",
		Value:     nil,
		Version:   0,
		CreatedAt: time.Time{},
		UpdatedAt: time.Time{},
		ExpiresAt: time.Time{},
	}
}

func validateKey(namespace string, key string) (entryKey, error) {
	normalizedNamespace, err := validateNamespace(namespace)
	if err != nil {
		return entryKey{namespace: "", key: ""}, err
	}
	if key == "" || len(key) > MaxKeyBytes || strings.ContainsRune(key, '\x00') {
		return entryKey{namespace: "", key: ""}, ErrInvalidKey
	}
	return entryKey{namespace: normalizedNamespace, key: key}, nil
}

func validateNamespace(namespace string) (string, error) {
	if namespace == "" || len(namespace) > MaxNamespaceBytes || strings.ContainsRune(namespace, '\x00') {
		return "", ErrInvalidNamespace
	}
	return namespace, nil
}
