package hotkv_test

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"unsafe"

	"goodkind.io/agent-gate/internal/hotkv"
)

func TestAggregateCapacityPreservesSuccessfulWrites(t *testing.T) {
	store := hotkv.New(hotkv.Options{MaxEntries: 2, MaxBytes: 10})
	defer store.Close()
	for _, key := range []string{"a", "b", "c"} {
		entry, stored, err := store.Set("ns", key, []byte("12"), hotkv.SetOptions{})
		if err != nil || !stored || string(entry.Value) != "12" {
			t.Fatalf("Set %s: stored=%v entry=%+v error=%v", key, stored, entry, err)
		}
		if _, found, err := store.Get("ns", key); err != nil || !found {
			t.Fatalf("successful Set did not retain %s: found=%v error=%v", key, found, err)
		}
	}
	entries, err := store.List("ns", "", 0, true)
	if err != nil || len(entries) != 2 || entries[0].Key != "b" || entries[1].Key != "c" {
		t.Fatalf("byte eviction: entries=%+v error=%v", entries, err)
	}
	if _, stored, err := store.Set("ns", "c", []byte("12345678"), hotkv.SetOptions{}); stored || !errors.Is(err, hotkv.ErrCapacity) {
		t.Fatalf("oversized replacement: stored=%v error=%v", stored, err)
	}
	entry, found, err := store.Get("ns", "c")
	if err != nil || !found || string(entry.Value) != "12" {
		t.Fatalf("failed ordinary write changed prior value: %+v %v %v", entry, found, err)
	}
	store.Configure(hotkv.Options{MaxBytes: 5})
	entries, err = store.List("ns", "", 0, true)
	if err != nil || len(entries) != 1 || entries[0].Key != "c" {
		t.Fatalf("configured byte reduction: entries=%+v error=%v", entries, err)
	}
	if _, stored, err := store.Set("ns", "c", []byte("34"), hotkv.SetOptions{Mode: hotkv.SetModeXX}); err != nil || !stored {
		t.Fatalf("exact-capacity replacement: stored=%v error=%v", stored, err)
	}
}

func TestCacheOwnsRetainedSubstringKeys(t *testing.T) {
	backing := strings.Repeat("x", 1024*1024)
	namespace := backing[:2]
	key := backing[2:3]
	store := hotkv.New(hotkv.Options{MaxBytes: 4})
	defer store.Close()
	for range 2 {
		if _, stored, err := store.Set(namespace, key, []byte("v"), hotkv.SetOptions{}); err != nil || !stored {
			t.Fatalf("Set: stored=%v error=%v", stored, err)
		}
	}
	entries, err := store.List(namespace, "", 0, false)
	if err != nil || len(entries) != 1 {
		t.Fatalf("List: %+v %v", entries, err)
	}
	if unsafe.StringData(entries[0].Namespace) == unsafe.StringData(namespace) || unsafe.StringData(entries[0].Key) == unsafe.StringData(key) {
		t.Fatal("retained key references the caller's larger allocation")
	}
}

func TestBoundedReadsRejectCompleteResultBeforeCloning(t *testing.T) {
	store := hotkv.New(hotkv.Options{})
	defer store.Close()
	for _, key := range []string{"b", "a"} {
		if _, _, err := store.Set("n", key, []byte("1234"), hotkv.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if entries, err := store.ListBounded("n", "", 0, true, 11, nil); entries != nil || !errors.Is(err, hotkv.ErrReadOverflow) {
		t.Fatalf("overflow returned partial result: %+v %v", entries, err)
	}
	entries, err := store.ListBounded("n", "", 0, true, 12, nil)
	if err != nil || len(entries) != 2 || entries[0].Key != "a" {
		t.Fatalf("exact budget: %+v %v", entries, err)
	}
	entries[0].Value[0] = 'x'
	entry, found, err := store.GetBounded("n", "a", true, 6, nil)
	if err != nil || !found || string(entry.Value) != "1234" {
		t.Fatalf("returned value aliases cache: %+v %v %v", entry, found, err)
	}
	if _, found, err := store.GetDeleteBounded("n", "a", true, 5, nil); found || !errors.Is(err, hotkv.ErrReadOverflow) {
		t.Fatalf("GetDelete overflow: %v %v", found, err)
	}
	if found, err := store.Exists("n", "a"); err != nil || !found {
		t.Fatalf("overflow deleted entry: %v %v", found, err)
	}
	sizer := func(metadata hotkv.Entry, valueBytes int) (int64, error) {
		if metadata.Value != nil || valueBytes != 0 {
			t.Errorf("metadata-only read included a value: %+v %d", metadata, valueBytes)
		}
		return 9, nil
	}
	entries, err = store.ListBounded("n", "", 1, false, 9, sizer)
	if err != nil || len(entries) != 1 || entries[0].Value != nil {
		t.Fatalf("metadata-only limited list: %+v %v", entries, err)
	}
	if _, err := store.ListBounded("n", "", 0, false, 17, sizer); !errors.Is(err, hotkv.ErrReadOverflow) {
		t.Fatalf("caller result accounting: %v", err)
	}
	if _, err := store.ListBounded("n", "", 0, true, -1, nil); !errors.Is(err, hotkv.ErrInvalidReadBudget) {
		t.Fatalf("negative read budget: %v", err)
	}
}

func TestMetadataOnlyListDoesNotAllocateValues(t *testing.T) {
	store := hotkv.New(hotkv.Options{})
	defer store.Close()
	if _, _, err := store.Set("n", "k", []byte(strings.Repeat("x", 1024*1024)), hotkv.SetOptions{}); err != nil {
		t.Fatal(err)
	}
	result := testing.Benchmark(func(benchmark *testing.B) {
		for range benchmark.N {
			if _, err := store.ListBounded("n", "", 0, false, 2, nil); err != nil {
				benchmark.Fatal(err)
			}
		}
	})
	if result.AllocedBytesPerOp() >= 64*1024 {
		t.Fatalf("metadata read allocated %d bytes for a 1 MiB value", result.AllocedBytesPerOp())
	}
}

func TestAggregateCapacityConcurrentWithConfigure(t *testing.T) {
	store := hotkv.New(hotkv.Options{MaxBytes: 32})
	defer store.Close()
	var group sync.WaitGroup
	for worker := range 4 {
		group.Go(func() {
			for index := range 200 {
				_, _, err := store.Set("n", fmt.Sprint(worker), []byte("12345678"), hotkv.SetOptions{})
				if err != nil && !errors.Is(err, hotkv.ErrCapacity) {
					t.Error(err)
				}
				store.Configure(hotkv.Options{MaxBytes: 4 + index%32})
			}
		})
	}
	group.Wait()
	store.Configure(hotkv.Options{MaxBytes: 4})
	if _, stored, err := store.Set("n", "k", []byte("123"), hotkv.SetOptions{}); stored || !errors.Is(err, hotkv.ErrCapacity) {
		t.Fatalf("reduced aggregate limit: stored=%v error=%v", stored, err)
	}
}
