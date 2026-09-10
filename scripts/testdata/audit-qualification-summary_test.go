package main

import "testing"

func TestPeakMetricsDistinguishMissingFromMeasuredZero(t *testing.T) {
	if _, available := peakCPU(result{}); available {
		t.Fatal("empty CPU intervals reported an available measurement")
	}
	if _, available := peakDisk(result{}); available {
		t.Fatal("empty disk intervals reported an available measurement")
	}
	measured := result{Intervals: []interval{{CPUNS: 0, DiskBytes: 0}}}
	if value, available := peakCPU(measured); !available || value != 0 {
		t.Fatalf("measured CPU = %.2f, available = %t", value, available)
	}
	if value, available := peakDisk(measured); !available || value != 0 {
		t.Fatalf("measured disk = %.2f, available = %t", value, available)
	}
}

func TestOptionalMetricFormattingReportsAvailability(t *testing.T) {
	if got := formatOptionalMedian(nil, 5); got != "unavailable (0/5 sampled)" {
		t.Fatalf("missing median = %q", got)
	}
	values := []float64{0, 10}
	if got := formatOptionalMedian(values, 5); got != "5.00 (2/5 sampled)" {
		t.Fatalf("available median = %q", got)
	}
	if got := formatOptionalRange(values, 5); got != "0.00..10.00 (2/5 sampled)" {
		t.Fatalf("available range = %q", got)
	}
}
