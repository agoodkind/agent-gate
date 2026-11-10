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
			entry, stored, err := store.Set("test", "key", []byte("1234"), hotkv.SetOptions{})
			if err != nil || !stored || string(entry.Value) != "1234" {
				t.Errorf("Set: stored=%v value=%q err=%v", stored, entry.Value, err)
				return
			}

			entry, found, err := store.Get("test", "key")
			if err != nil || !found || string(entry.Value) != "1234" {
				t.Errorf("Get: found=%v value=%q err=%v", found, entry.Value, err)
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

	entry, found, err := store.Get("test", "key")
	if err != nil || !found || string(entry.Value) != "1234" {
		t.Fatalf("Get after limit reduction: found=%v value=%q err=%v", found, entry.Value, err)
	}
}
