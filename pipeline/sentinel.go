package pipeline

import "context"

// Sentinel guards a named operation with health bookkeeping the implementation chooses.
type Sentinel interface {
	Probe(ctx context.Context, name string, fn func(context.Context) error) error
}

// NoopSentinel runs fn directly and returns its error verbatim.
type NoopSentinel struct{}

// Probe calls fn(ctx) and returns the result without any extra bookkeeping.
func (NoopSentinel) Probe(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}
