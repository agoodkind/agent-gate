package evaluation

import (
	"testing"
	"time"
)

func TestEstimatedCostMicros(t *testing.T) {
	mini := ModelPricing{InputPerMillion: 0.15, CachedInputPerMillion: 0.015, OutputPerMillion: 0.60}
	free := ModelPricing{}

	tests := []struct {
		name       string
		prompt     int64
		cached     int64
		completion int64
		price      ModelPricing
		want       int64
	}{
		{name: "mini full input upper bound", prompt: 104344, cached: 0, completion: 9705, price: mini, want: 21475},
		{name: "single mini call rounds", prompt: 3508, cached: 0, completion: 202, price: mini, want: 647},
		{name: "cached tokens bill cheaper", prompt: 1000, cached: 900, completion: 100, price: mini, want: 89},
		{name: "cached clamped to prompt", prompt: 100, cached: 500, completion: 0, price: mini, want: 2},
		{name: "negative cached treated as zero", prompt: 1000, cached: -5, completion: 0, price: mini, want: 150},
		{name: "free model costs nothing", prompt: 500000, cached: 0, completion: 5000, price: free, want: 0},
		{name: "zero tokens", prompt: 0, cached: 0, completion: 0, price: mini, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := estimatedCostMicros(test.prompt, test.cached, test.completion, test.price)
			if got != test.want {
				t.Fatalf("estimatedCostMicros(%d,%d,%d) = %d, want %d",
					test.prompt, test.cached, test.completion, got, test.want)
			}
		})
	}
}

func TestDedupCacheHitRate(t *testing.T) {
	if rate := (DedupCacheStats{}).HitRate(); rate != 0 {
		t.Fatalf("empty cache hit rate = %v, want 0", rate)
	}
	if rate := (DedupCacheStats{Hits: 1, Misses: 3}).HitRate(); rate != 0.25 {
		t.Fatalf("hit rate = %v, want 0.25", rate)
	}
	if rate := (DedupCacheStats{Hits: 2, Misses: 0}).HitRate(); rate != 1 {
		t.Fatalf("all-hit rate = %v, want 1", rate)
	}
}

func TestObservedWindowDays(t *testing.T) {
	if days := observedWindowDays(time.Time{}, time.Time{}, time.Time{}, time.Time{}); days != 1 {
		t.Fatalf("no bounds window = %v, want 1 floor", days)
	}
	start := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)
	if days := observedWindowDays(time.Time{}, time.Time{}, start, end); days != 10 {
		t.Fatalf("observed span = %v, want 10", days)
	}
	if days := observedWindowDays(start, end, time.Time{}, time.Time{}); days != 10 {
		t.Fatalf("filter span = %v, want 10", days)
	}
}
