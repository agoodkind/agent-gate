package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

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

// attrAdapter and attrReason are the attribute keys used on the failure counter.
var (
	attrAdapter = attribute.Key("adapter")
	attrReason  = attribute.Key("reason")
)

// MeteringSentinel increments an OTel counter on each probe failure.
type MeteringSentinel struct {
	counter metric.Int64Counter
}

// NewMeteringSentinel creates a MeteringSentinel using the global OTel meter.
// The counter name is "agent_gate_sentinel_failures_total" with attributes
// "adapter" (the probe name) and "reason" (a short classification of the error).
func NewMeteringSentinel() (*MeteringSentinel, error) {
	counter, err := otel.Meter("agent-gate").Int64Counter(
		"agent_gate_sentinel_failures_total",
		metric.WithDescription("Total number of sentinel probe failures by adapter and reason."),
		metric.WithUnit("{failure}"),
	)
	if err != nil {
		slog.Error("sentinel counter init failed", slog.Any("err", err))
		return nil, fmt.Errorf("create sentinel failures counter: %w", err)
	}
	return &MeteringSentinel{counter: counter}, nil
}

// Probe calls fn(ctx). If fn returns a non-nil error, it increments the failure
// counter with the adapter name and a short reason derived from the error, then
// returns the original error unchanged.
func (s *MeteringSentinel) Probe(ctx context.Context, name string, fn func(context.Context) error) error {
	err := fn(ctx)
	if err != nil {
		s.counter.Add(ctx, 1,
			metric.WithAttributes(
				attrAdapter.String(name),
				attrReason.String(classifyError(err)),
			),
		)
	}
	return err
}

// classifyError returns a short lowercase label for the error suitable for use
// as a metric attribute. It checks for well-known sentinel types before falling
// back to a prefix of the error message.
func classifyError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	msg := err.Error()
	// Use the first token of the error message, capped to 32 characters, so
	// high-cardinality messages do not blow up metric storage.
	first := strings.FieldsFunc(msg, func(r rune) bool {
		return r == ':' || r == ' ' || r == '\n'
	})
	if len(first) == 0 {
		return "unknown"
	}
	label := strings.ToLower(first[0])
	if len(label) > 32 {
		label = label[:32]
	}
	return label
}
