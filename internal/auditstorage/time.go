package auditstorage

import "time"

// FormatTime preserves nanoseconds in a fixed-width UTC sort key.
func FormatTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
