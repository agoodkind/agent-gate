package auditstorage

import (
	"testing"
	"time"
)

func TestBucketStartUsesEpochAlignedUTC(t *testing.T) {
	interval := 24 * time.Hour
	input := time.Date(2026, 9, 8, 23, 30, 0, 0, time.FixedZone("PDT", -7*3600))
	want := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if got := bucketStart(input, interval); !got.Equal(want) {
		t.Fatalf("start = %v, want %v", got, want)
	}
}

func TestBucketStartBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		now      string
		interval time.Duration
		want     string
	}{
		{"epoch", "1970-01-01T00:00:00Z", 24 * time.Hour, "1970-01-01T00:00:00Z"},
		{"before epoch", "1969-12-31T23:59:59Z", 24 * time.Hour, "1969-12-31T00:00:00Z"},
		{"midnight", "2026-09-09T00:00:00Z", 24 * time.Hour, "2026-09-09T00:00:00Z"},
		{"spring", "2026-03-08T03:30:00-07:00", 24 * time.Hour, "2026-03-08T00:00:00Z"},
		{"fall", "2026-11-01T01:30:00-08:00", 24 * time.Hour, "2026-11-01T00:00:00Z"},
		{"non day interval", "1970-01-01T06:59:59Z", 7 * time.Hour, "1970-01-01T00:00:00Z"},
	} {
		t.Run(test.name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, test.now)
			if err != nil {
				t.Fatal(err)
			}
			want, err := time.Parse(time.RFC3339, test.want)
			if err != nil {
				t.Fatal(err)
			}
			if got := bucketStart(now, test.interval); !got.Equal(want) {
				t.Fatalf("start = %v, want %v", got, want)
			}
		})
	}
}
