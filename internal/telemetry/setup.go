// Package telemetry wires up the process-wide OTel tracer and meter.
package telemetry

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"goodkind.io/gklog/trace"
)

// Options configures Setup. Zero values are valid; an empty OTLPEndpoint
// disables OTLP export while still installing the global providers so that
// trace_id / span_id flow into logs.
type Options struct {
	OTLPEndpoint      string
	SlowOpThresholdMs int
}

// Setup installs the global OTel TracerProvider (via gklog/trace) and a
// no-op MeterProvider. The returned [io.Closer] flushes both on shutdown.
func Setup(opts Options) (io.Closer, error) {
	if opts.SlowOpThresholdMs > 0 {
		trace.SlowOpThreshold = time.Duration(opts.SlowOpThresholdMs) * time.Millisecond
	}
	traceCloser, err := trace.Setup(trace.Options{
		ServiceName: "agent-gate",
		Endpoint:    opts.OTLPEndpoint,
	})
	if err != nil {
		slog.Error("trace setup failed", "err", err)
		return nil, fmt.Errorf("trace setup: %w", err)
	}

	mp := sdkmetric.NewMeterProvider()
	otel.SetMeterProvider(mp)

	return &telemetryCloser{traceCloser: traceCloser, mp: mp}, nil
}

type telemetryCloser struct {
	traceCloser io.Closer
	mp          *sdkmetric.MeterProvider
}

func (c *telemetryCloser) Close() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mpErr := c.mp.Shutdown(ctx)
	if mpErr != nil {
		slog.Error("meter provider shutdown failed", "err", mpErr)
	}

	traceErr := c.traceCloser.Close()
	if traceErr != nil {
		slog.Error("trace provider shutdown failed", "err", traceErr)
	}

	if mpErr != nil || traceErr != nil {
		return fmt.Errorf("telemetry close: meter=%w trace=%w", mpErr, traceErr)
	}
	return nil
}
