package hotkv

import "errors"

// ObservationOrder orders receipts within successive current-file assignments.
type ObservationOrder struct {
	Generation uint64
	ReceiptID  int64
}

func (order ObservationOrder) newerThan(other ObservationOrder) bool {
	if order.Generation != other.Generation {
		return order.Generation > other.Generation
	}

	return order.ReceiptID > other.ReceiptID
}

// RegisterObservation records a hot event before it can publish temporal state.
// Receipt zero advances the current assignment without publishing a value.
func (s *Store) RegisterObservation(order ObservationOrder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerObservationLocked(order)
}

func (s *Store) registerObservationLocked(order ObservationOrder) {
	if order.ReceiptID >= 0 && order.newerThan(s.highestRegistered) {
		s.highestRegistered = order
	}
}

// Observe publishes a newer value or an unavailable marker under one cache lock.
// It rejects retired assignments and observations that removal fences exclude.
// A true result means the cache retained the value or its unavailable marker.
func (s *Store) Observe(namespace, key string, order ObservationOrder, value string) (bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return false, err
	}

	if order.ReceiptID <= 0 {
		return false, errors.New("observation receipt must be positive")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.registerObservationLocked(order)
	if order.Generation < s.highestRegistered.Generation {
		return false, nil
	}

	now := s.now()
	current, exists := s.entryLocked(storedKey, now)
	temporalExists := exists && current.order.ReceiptID > 0
	if temporalExists && !order.newerThan(current.order) {
		return false, nil
	}

	if !temporalExists && !order.newerThan(s.discardedBefore) {
		return false, nil
	}

	available := len(value) <= s.options.MaxValueBytes && s.fitsLocked(storedKey, len(value))
	if !available {
		value = ""
	}

	if !s.prepareWriteLocked(storedKey, len(value), now) {
		s.removeLocked(storedKey)
		// Losing an unavailable marker must fence delayed observations too.
		s.discardedBefore = s.highestRegistered

		return false, ErrCapacity
	}

	// Conversion and retained allocation happen only after capacity admission.
	current = s.writeLocked(storedKey, []byte(value), now, 0)
	current.order = order
	current.unavailable = !available

	return true, nil
}

// Observation returns an owned value only when temporal context is available.
func (s *Store) Observation(namespace, key string) (string, bool, error) {
	storedKey, err := validateKey(namespace, key)
	if err != nil {
		return "", false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current, exists := s.entryLocked(storedKey, s.now())
	if !exists || current.unavailable || current.order.ReceiptID <= 0 {
		return "", false, nil
	}

	return string(current.value), true, nil
}
