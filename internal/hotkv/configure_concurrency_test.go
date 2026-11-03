package hotkv_test

import (
	"errors"
	"sync"
	"testing"

	"goodkind.io/agent-gate/internal/hotkv"
)

func TestSetConcurrentWithConfigure(t *testing.T) {
	store := hotkv.New(hotkv.Options{MaxValueBytes: 8})
	defer store.Close()
	start := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 1000; i++ {
			_, _, err := store.Set("test", "key", []byte("12345678"), hotkv.SetOptions{})
			if err != nil && !errors.Is(err, hotkv.ErrValueTooLarge) {
				t.Errorf("Set: %v", err)
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < 1000; i++ {
			store.Configure(hotkv.Options{MaxValueBytes: 4})
			store.Configure(hotkv.Options{MaxValueBytes: 8})
		}
	}()
	close(start)
	workers.Wait()
	store.Configure(hotkv.Options{MaxValueBytes: 4})
	_, stored, err := store.Set("test", "key", []byte("12345678"), hotkv.SetOptions{})
	if stored || !errors.Is(err, hotkv.ErrValueTooLarge) {
		t.Fatalf("Set after limit reduction: stored=%v err=%v", stored, err)
	}
	_, found, err := store.Get("test", "key")
	if err != nil || found {
		t.Fatalf("Get after limit reduction: found=%v err=%v", found, err)
	}
}
