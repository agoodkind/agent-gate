package pipeline

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

func TestNoopSentinelPassesNilThrough(t *testing.T) {
	t.Parallel()
	var sentinel NoopSentinel
	called := false
	err := sentinel.Probe(context.Background(), "probe", func(_ context.Context) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if !called {
		t.Fatalf("fn should have been called")
	}
}

func TestNoopSentinelPassesErrorThrough(t *testing.T) {
	t.Parallel()
	var sentinel NoopSentinel
	boom := errors.New("boom")
	err := sentinel.Probe(context.Background(), "probe", func(_ context.Context) error {
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("expected boom, got %v", err)
	}
}

func TestNoopSentinelForwardsContext(t *testing.T) {
	t.Parallel()
	var sentinel NoopSentinel
	type ctxKey string
	const key ctxKey = "k"
	ctx := context.WithValue(context.Background(), key, "value")
	err := sentinel.Probe(ctx, "probe", func(inner context.Context) error {
		if inner.Value(key) != "value" {
			t.Fatalf("ctx value not forwarded")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// newTestMeterProvider wires a ManualReader into a fresh MeterProvider, installs
// it as the global provider, and returns both so tests can collect data and
// restore state afterward.
func newTestMeterProvider(t *testing.T) (*sdkmetric.ManualReader, func()) {
	t.Helper()
	prev := otel.GetMeterProvider()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(resource.Empty()),
	)
	otel.SetMeterProvider(mp)
	return reader, func() {
		otel.SetMeterProvider(prev)
	}
}

// collectCounterPoints collects metric data and returns the data points for the
// named counter. It fails the test if collection errors or the counter is absent.
func collectCounterPoints(t *testing.T, reader *sdkmetric.ManualReader, counterName string) []metricdata.DataPoint[int64] {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect error: %v", err)
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != counterName {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %q has unexpected data type %T", counterName, m.Data)
			}
			return sum.DataPoints
		}
	}
	// Return an empty slice when the counter has never been incremented; the
	// OTel SDK does not emit a data point until the first Add call.
	return nil
}

// TestMeteringSentinelIncrementsOnFailure verifies that a failing probe
// increments the counter exactly once with the correct adapter attribute.
func TestMeteringSentinelIncrementsOnFailure(t *testing.T) {
	reader, restore := newTestMeterProvider(t)
	defer restore()

	s, err := NewMeteringSentinel()
	if err != nil {
		t.Fatalf("NewMeteringSentinel: %v", err)
	}

	probeErr := errors.New("connection refused")
	_ = s.Probe(context.Background(), "gitleaks", func(_ context.Context) error {
		return probeErr
	})

	pts := collectCounterPoints(t, reader, "agent_gate_sentinel_failures_total")
	if len(pts) != 1 {
		t.Fatalf("expected 1 data point, got %d", len(pts))
	}
	if pts[0].Value != 1 {
		t.Fatalf("expected counter value 1, got %d", pts[0].Value)
	}

	// Confirm adapter attribute is set correctly.
	adapterVal, adapterOK := pts[0].Attributes.Value(attrAdapter)
	if !adapterOK || adapterVal.AsString() != "gitleaks" {
		t.Fatalf("expected adapter=gitleaks, got %v (ok=%v)", adapterVal, adapterOK)
	}

	// Confirm reason attribute is present and non-empty.
	reasonVal, reasonOK := pts[0].Attributes.Value(attrReason)
	if !reasonOK || reasonVal.AsString() == "" {
		t.Fatalf("expected non-empty reason, got %v (ok=%v)", reasonVal, reasonOK)
	}
}

// TestMeteringSentinelNoopOnSuccess verifies that a successful probe does not
// increment the counter.
func TestMeteringSentinelNoopOnSuccess(t *testing.T) {
	reader, restore := newTestMeterProvider(t)
	defer restore()

	s, err := NewMeteringSentinel()
	if err != nil {
		t.Fatalf("NewMeteringSentinel: %v", err)
	}

	_ = s.Probe(context.Background(), "entropy", func(_ context.Context) error {
		return nil
	})

	pts := collectCounterPoints(t, reader, "agent_gate_sentinel_failures_total")
	if len(pts) != 0 {
		t.Fatalf("expected 0 data points on success, got %d", len(pts))
	}
}

// TestMeteringSentinelPropagatesError confirms that the original error is
// returned regardless of counter instrumentation.
func TestMeteringSentinelPropagatesError(t *testing.T) {
	_, restore := newTestMeterProvider(t)
	defer restore()

	s, err := NewMeteringSentinel()
	if err != nil {
		t.Fatalf("NewMeteringSentinel: %v", err)
	}

	sentinel := errors.New("health check failed")
	got := s.Probe(context.Background(), "trufflehog", func(_ context.Context) error {
		return sentinel
	})
	if !errors.Is(got, sentinel) {
		t.Fatalf("expected sentinel error, got %v", got)
	}
}
