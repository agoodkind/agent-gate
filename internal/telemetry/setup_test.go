package telemetry_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/telemetry"
)

func TestSetupEmptyOptionsReturnsCloser(t *testing.T) {
	closer, err := telemetry.Setup(telemetry.Options{})
	if err != nil {
		t.Fatalf("Setup with empty options returned error: %v", err)
	}
	if closer == nil {
		t.Fatal("Setup with empty options returned nil closer")
	}
	if closeErr := closer.Close(); closeErr != nil {
		t.Fatalf("Close returned error: %v", closeErr)
	}
}

func TestSetupWithEndpointDoesNotPanic(t *testing.T) {
	// The endpoint is unreachable; Setup should not panic because the OTLP
	// exporter connection happens asynchronously in the batch exporter.
	closer, err := telemetry.Setup(telemetry.Options{
		OTLPEndpoint:      "127.0.0.1:4317",
		SlowOpThresholdMs: 50,
	})
	if err != nil {
		// An error is allowed when the endpoint is genuinely unreachable at
		// connection time, but a panic is not.
		t.Logf("Setup with endpoint returned error (expected when unreachable): %v", err)
		return
	}
	if closer == nil {
		t.Fatal("Setup with endpoint returned nil closer")
	}
	_ = closer.Close()
}
