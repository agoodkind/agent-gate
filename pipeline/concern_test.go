package pipeline

import (
	"testing"
	"time"
)

func TestProfileRoundtrip(t *testing.T) {
	t.Parallel()
	profile := Profile{
		Name:         "regex.aws_secret",
		Cost:         CostMedium,
		Idempotent:   true,
		DependsOn:    []string{"loader"},
		Timeout:      250 * time.Millisecond,
		MemoLifetime: MemoEvent,
		MemoTTL:      0,
	}
	if profile.Name != "regex.aws_secret" {
		t.Fatalf("Name not preserved: %q", profile.Name)
	}
	if profile.Cost != CostMedium {
		t.Fatalf("Cost not preserved: %d", profile.Cost)
	}
	if !profile.Idempotent {
		t.Fatalf("Idempotent not preserved")
	}
	if len(profile.DependsOn) != 1 || profile.DependsOn[0] != "loader" {
		t.Fatalf("DependsOn not preserved: %v", profile.DependsOn)
	}
	if profile.Timeout != 250*time.Millisecond {
		t.Fatalf("Timeout not preserved: %s", profile.Timeout)
	}
	if profile.MemoLifetime != MemoEvent {
		t.Fatalf("MemoLifetime not preserved: %s", profile.MemoLifetime)
	}
}

func TestCostClassOrdering(t *testing.T) {
	t.Parallel()
	if !(CostCheap < CostMedium && CostMedium < CostExpensive && CostExpensive < CostExternal) {
		t.Fatalf("CostClass values not in ascending order")
	}
}

func TestMemoLifetimeConstants(t *testing.T) {
	t.Parallel()
	if MemoEvent != "event" {
		t.Fatalf("MemoEvent constant changed: %q", MemoEvent)
	}
	if MemoSession != "session" {
		t.Fatalf("MemoSession constant changed: %q", MemoSession)
	}
}
