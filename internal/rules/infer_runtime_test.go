package rules

import (
	"context"
	"testing"
	"time"
)

func TestInferSingleflightClassifiesFollowerContextErrors(t *testing.T) {
	tests := []struct {
		name       string
		context    func() (context.Context, context.CancelFunc)
		errorClass string
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			errorClass: "canceled",
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			errorClass: "deadline_exceeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := NewInferRuntimeWithCache(nil, nil)
			t.Cleanup(runtime.Close)
			runtime.inflight["same"] = &inferFlight{done: make(chan struct{})}
			ctx, cancel := test.context()
			defer cancel()

			result := runtime.singleflight(ctx, "same", func() inferResult {
				t.Fatal("follower ran leader function")
				return emptyInferResult()
			})

			if result.errorClass != test.errorClass {
				t.Fatalf("error class = %q, want %q", result.errorClass, test.errorClass)
			}
		})
	}
}
