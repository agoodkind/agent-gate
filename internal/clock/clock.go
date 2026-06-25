// Package clock centralizes wall-clock access for production code and tests.
package clock

import "time"

// Now returns the current wall-clock time.
func Now() time.Time {
	return time.Now()
}

// Until returns the duration until the target time.
func Until(target time.Time) time.Duration {
	return time.Until(target)
}
