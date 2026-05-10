package pipeline

import (
	"errors"
	"testing"
	"time"
)

func TestFixedSchedulerSlots(t *testing.T) {
	t.Parallel()
	scheduler := FixedScheduler{SlotCount: 4}
	if got := scheduler.Slots(Profile{Name: "a", Cost: CostCheap}); got != 4 {
		t.Fatalf("Slots cheap: want 4, got %d", got)
	}
	if got := scheduler.Slots(Profile{Name: "b", Cost: CostExternal}); got != 4 {
		t.Fatalf("Slots external: want 4, got %d", got)
	}
}

func TestFixedSchedulerObserveIsNoop(t *testing.T) {
	t.Parallel()
	scheduler := FixedScheduler{SlotCount: 1}
	scheduler.Observe("anything", 10*time.Millisecond, errors.New("ignored"))
}
