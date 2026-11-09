package hotkv_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/hotkv"
)

func observationOrder(receipt int64) hotkv.ObservationOrder {
	return hotkv.ObservationOrder{Generation: 1, ReceiptID: receipt}
}

func publishObservation(t *testing.T, store *hotkv.Store, key string, receipt int64, value string) {
	t.Helper()
	if stored, err := store.Observe("n", key, observationOrder(receipt), value); err != nil ||
		!stored {
		t.Fatalf("Observe %s: stored=%v error=%v", key, stored, err)
	}
}

func TestTemporalEvictionFencesDelayedPublications(t *testing.T) {
	for _, options := range []hotkv.Options{{MaxEntries: 1}, {MaxBytes: 4}} {
		store := hotkv.New(options)
		publishObservation(t, store, "a", 1, "v")
		store.RegisterObservation(observationOrder(10))
		publishObservation(t, store, "b", 3, "v")
		if stored, err := store.Observe("n", "a", observationOrder(2), "old"); err != nil ||
			stored {
			t.Fatalf("delayed publication restored evicted state: %v %v", stored, err)
		}

		if value, available, err := store.Observation("n", "b"); err != nil || !available ||
			value != "v" {
			t.Fatalf(
				"publication evicted itself after raising the floor: %q %v %v",
				value,
				available,
				err,
			)
		}

		publishObservation(t, store, "b", 4, "w")
		store.Close()
	}
}

func TestTemporalUnavailableMarkersFenceOlderValues(t *testing.T) {
	for _, maxBytes := range []int{2, 3, 8} {
		store := hotkv.New(hotkv.Options{
			MaxValueBytes: 2,
			MaxBytes:      8,
		})
		publishObservation(t, store, "a", 1, "ok")
		store.Configure(hotkv.Options{
			MaxValueBytes: 2,
			MaxBytes:      maxBytes,
		})
		stored, err := store.Observe("n", "a", observationOrder(3), strings.Repeat("x", 20))
		if err != nil || !stored {
			t.Fatalf("marker at capacity %d: stored=%v error=%v", maxBytes, stored, err)
		}

		if value, available, err := store.Observation("n", "a"); err != nil || available ||
			value != "" {
			t.Fatalf("oversized update retained stale content: %q %v %v", value, available, err)
		}

		if stored, err := store.Observe("n", "a", observationOrder(2), "ok"); err != nil || stored {
			t.Fatalf("older update replaced marker: %v %v", stored, err)
		}

		store.RegisterObservation(observationOrder(5))
		if _, err := store.Delete("n", "a"); err != nil {
			t.Fatal(err)
		}

		if stored, err := store.Observe("n", "a", observationOrder(4), "ok"); err != nil || stored {
			t.Fatalf("older update replaced evicted marker: %v %v", stored, err)
		}

		store.Close()
	}

	store := hotkv.New(hotkv.Options{MaxBytes: 8})
	defer store.Close()
	publishObservation(t, store, "a", 1, "v")
	store.Configure(hotkv.Options{MaxBytes: 1})
	if stored, err := store.Observe("n", "a", observationOrder(3), "new"); stored ||
		!errors.Is(err, hotkv.ErrCapacity) {
		t.Fatalf("marker cannot fit: %v %v", stored, err)
	}

	store.Configure(hotkv.Options{MaxBytes: 8})
	if stored, err := store.Observe("n", "a", observationOrder(2), "old"); err != nil || stored {
		t.Fatalf("restored capacity admitted stale context: %v %v", stored, err)
	}

	publishObservation(t, store, "a", 4, "new")
}

func TestTemporalFloorPreservesUnrelatedRetainedKeys(t *testing.T) {
	store := hotkv.New(hotkv.Options{})
	defer store.Close()
	publishObservation(t, store, "removed", 1, "one")
	publishObservation(t, store, "retained", 2, "two")
	store.RegisterObservation(observationOrder(100))
	if _, err := store.Delete("n", "removed"); err != nil {
		t.Fatal(err)
	}

	publishObservation(t, store, "retained", 3, "three")
	if stored, err := store.Observe("n", "missing", observationOrder(4), "four"); err != nil ||
		stored {
		t.Fatalf("missing key ignored removal fence: %v %v", stored, err)
	}

	if value, available, err := store.Observation("n", "retained"); err != nil || !available ||
		value != "three" {
		t.Fatalf("unrelated retained key lost ordering: %q %v %v", value, available, err)
	}
}

func TestOrdinaryEvictionDoesNotFenceObservations(t *testing.T) {
	store := hotkv.New(hotkv.Options{MaxEntries: 1})
	defer store.Close()
	store.RegisterObservation(observationOrder(100))

	for _, key := range []string{"one", "two"} {
		if _, _, err := store.Set("ordinary", key, []byte("v"), hotkv.SetOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	publishObservation(t, store, "temporal", 2, "two")
}

func TestTemporalRemovalPathsFenceDelayedUpdates(t *testing.T) {
	removals := map[string]func(*testing.T, *hotkv.Store){
		"delete": func(t *testing.T, store *hotkv.Store) {
			_, err := store.Delete("n", "k")
			if err != nil {
				t.Fatal(err)
			}
		},
		"get delete": func(t *testing.T, store *hotkv.Store) {
			_, _, err := store.GetDelete("n", "k")
			if err != nil {
				t.Fatal(err)
			}
		},
		"bounded get delete": func(t *testing.T, store *hotkv.Store) {
			_, _, err := store.GetDeleteBounded("n", "k", false, 2, nil)
			if err != nil {
				t.Fatal(err)
			}
		},
		"expire delete": func(t *testing.T, store *hotkv.Store) {
			_, err := store.Expire("n", "k", 0)
			if err != nil {
				t.Fatal(err)
			}
		},
		"configure value": func(_ *testing.T, store *hotkv.Store) {
			store.Configure(hotkv.Options{MaxValueBytes: 1})
		},
		"configure bytes": func(_ *testing.T, store *hotkv.Store) {
			store.Configure(hotkv.Options{MaxBytes: 1})
		},
		"configure entries": func(t *testing.T, store *hotkv.Store) {
			publishObservation(t, store, "other", 4, "v")
			store.Configure(hotkv.Options{MaxEntries: 1})
		},
		"ordinary overwrite": func(t *testing.T, store *hotkv.Store) {
			_, _, err := store.Set("n", "k", []byte("ordinary"), hotkv.SetOptions{})
			if err != nil {
				t.Fatal(err)
			}
		},
	}

	for _, read := range []string{"get", "exists", "pttl", "list", "prune", "configure", "periodic"} {
		removals["expiry "+read] = func(t *testing.T, store *hotkv.Store) {
			if read == "periodic" {
				store.Configure(hotkv.Options{PruneInterval: time.Millisecond})
			}

			if _, err := store.Expire("n", "k", time.Nanosecond); err != nil {
				t.Fatal(err)
			}

			time.Sleep(time.Millisecond)
			switch read {
			case "get":
				_, _, _ = store.Get("n", "k")
			case "exists":
				_, _ = store.Exists("n", "k")
			case "pttl":
				_, _, _, _ = store.PTTL("n", "k")
			case "list":
				_, _ = store.List("n", "", 0, false)
			case "prune":
				store.PruneExpired()
			case "configure":
				store.Configure(hotkv.Options{})
			case "periodic":
				time.Sleep(5 * time.Millisecond)
			}
		}
	}

	for name, remove := range removals {
		t.Run(
			name,
			func(t *testing.T) {
				store := hotkv.New(hotkv.Options{})
				defer store.Close()
				publishObservation(t, store, "k", 1, "old")
				store.RegisterObservation(observationOrder(5))
				remove(t, store)
				store.Configure(hotkv.Options{})
				if stored, err := store.Observe(
					"n",
					"k",
					observationOrder(3),
					"late",
				); err != nil ||
					stored {
					t.Fatalf("removal allowed delayed publication: %v %v", stored, err)
				}

				publishObservation(t, store, "k", 6, "current")
			},
		)
	}
}

func TestTemporalGenerationRejectsRetiredAssignments(t *testing.T) {
	store := hotkv.New(hotkv.Options{})
	defer store.Close()
	publishObservation(t, store, "k", 100, "previous file")
	store.RegisterObservation(hotkv.ObservationOrder{Generation: 2})

	for _, key := range []string{"k", "missing"} {
		if stored, err := store.Observe("n", key, observationOrder(101), "late"); err != nil ||
			stored {
			t.Fatalf("retired assignment published %s: %v %v", key, stored, err)
		}
	}

	if stored, err := store.Observe(
		"n",
		"k",
		hotkv.ObservationOrder{Generation: 2, ReceiptID: 1},
		"new file",
	); err != nil ||
		!stored {
		t.Fatalf("new file receipt did not replace previous file: %v %v", stored, err)
	}

	for generation := uint64(3); generation < 1000; generation++ {
		order := hotkv.ObservationOrder{Generation: generation, ReceiptID: 1}
		if stored, err := store.Observe("n", "k", order, fmt.Sprint(generation)); err != nil ||
			!stored {
			t.Fatalf("successive assignment %d: %v %v", generation, stored, err)
		}
	}
}

func TestTemporalAggregateOverflowReplacesPriorContent(t *testing.T) {
	store := hotkv.New(hotkv.Options{
		MaxValueBytes: 100,
		MaxBytes:      8,
	})
	defer store.Close()
	publishObservation(t, store, "a", 1, "old")
	publishObservation(t, store, "a", 3, "exceeds aggregate capacity")
	if value, available, err := store.Observation("n", "a"); err != nil || available ||
		value != "" {
		t.Fatalf("aggregate overflow retained old content: %q %v %v", value, available, err)
	}

	if stored, err := store.Observe("n", "a", observationOrder(2), "late"); err != nil || stored {
		t.Fatalf("aggregate overflow admitted an older update: %v %v", stored, err)
	}
}
