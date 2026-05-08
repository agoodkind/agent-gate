package pipeline

import (
	"context"
	"errors"
	"testing"
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
