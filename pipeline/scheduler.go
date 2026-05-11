package pipeline

import "time"

// Scheduler decides how many concurrent slots a Condition may use and observes outcomes.
type Scheduler interface {
	Slots(profile Profile) int
	Observe(name string, elapsed time.Duration, err error)
}

// FixedScheduler returns a constant slot count and discards observations.
type FixedScheduler struct {
	SlotCount int
}

// Slots returns the configured constant slot count.
func (s FixedScheduler) Slots(_ Profile) int {
	return s.SlotCount
}

// Observe is a no-op for the fixed scheduler.
func (s FixedScheduler) Observe(_ string, _ time.Duration, _ error) {
}
