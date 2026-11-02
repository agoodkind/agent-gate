package auditstorage

import (
	"context"
	"errors"
)

// CancellationOnly reports whether every leaf represents context cancellation
// or expiration. A joined cleanup failure requires a diagnostic.
func CancellationOnly(err error) bool {
	return matchErrorLeaves(err, func(cause error) bool {
		return errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded)
	})
}

// MatchesOnly reports whether every leaf matches the required target.
func MatchesOnly(err, target error) bool {
	if target == nil {
		return false
	}
	return matchErrorLeaves(err, func(cause error) bool { return errors.Is(cause, target) })
}

func matchErrorLeaves(err error, matches func(error) bool) bool {
	if err == nil {
		return false
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !matchErrorLeaves(cause, matches) {
				return false
			}
		}
		return true
	}
	var wrapped interface{ Unwrap() error }
	if errors.As(err, &wrapped) {
		return matchErrorLeaves(wrapped.Unwrap(), matches)
	}
	return matches(err)
}
