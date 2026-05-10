package pipeline

import (
	"bytes"
	"context"
	"testing"
)

func TestEventCacheGetPutRoundtrip(t *testing.T) {
	t.Parallel()
	cache := NewEventCache()
	handle := cache.For(context.Background(), MemoEvent, 0)

	if _, ok := handle.Get("missing"); ok {
		t.Fatalf("Get on empty cache should return ok=false")
	}

	value := []byte("payload")
	handle.Put("k", value)
	got, ok := handle.Get("k")
	if !ok {
		t.Fatalf("Get after Put should return ok=true")
	}
	if !bytes.Equal(got, value) {
		t.Fatalf("Get returned %q, want %q", got, value)
	}

	handle.Put("k", []byte("replaced"))
	got, _ = handle.Get("k")
	if !bytes.Equal(got, []byte("replaced")) {
		t.Fatalf("Get after overwrite returned %q", got)
	}
}

func TestEventCacheForReturnsSelf(t *testing.T) {
	t.Parallel()
	cache := NewEventCache()
	first := cache.For(context.Background(), MemoEvent, 0)
	second := cache.For(context.Background(), MemoSession, 0)
	first.Put("k", []byte("v"))
	got, ok := second.Get("k")
	if !ok || !bytes.Equal(got, []byte("v")) {
		t.Fatalf("two handles from one EventCache should share storage in Landing 1")
	}
}
