package hotkv

import (
	"errors"
	"testing"
	"time"
)

func TestStoreSetGetAndDelete(t *testing.T) {
	store := New(Options{})
	defer store.Close()

	entry, stored, err := store.Set("exec", "repo", []byte("indexed"), SetOptions{})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !stored {
		t.Fatal("Set stored = false, want true")
	}
	if entry.Version != 1 {
		t.Fatalf("version = %d, want 1", entry.Version)
	}

	got, found, err := store.Get("exec", "repo")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !found || string(got.Value) != "indexed" {
		t.Fatalf("Get found=%v value=%q, want indexed", found, string(got.Value))
	}

	deleted, err := store.Delete("exec", "repo")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !deleted {
		t.Fatal("Delete deleted = false, want true")
	}

	_, found, err = store.Get("exec", "repo")
	if err != nil {
		t.Fatalf("Get after delete: %v", err)
	}
	if found {
		t.Fatal("Get after delete found = true, want false")
	}
}

func TestStoreSetNXAndXX(t *testing.T) {
	store := New(Options{})
	defer store.Close()

	_, stored, err := store.Set("exec", "repo", []byte("one"), SetOptions{Mode: SetModeXX})
	if err != nil {
		t.Fatalf("Set XX missing: %v", err)
	}
	if stored {
		t.Fatal("Set XX missing stored = true, want false")
	}

	_, stored, err = store.Set("exec", "repo", []byte("one"), SetOptions{Mode: SetModeNX})
	if err != nil {
		t.Fatalf("Set NX missing: %v", err)
	}
	if !stored {
		t.Fatal("Set NX missing stored = false, want true")
	}

	_, stored, err = store.Set("exec", "repo", []byte("two"), SetOptions{Mode: SetModeNX})
	if err != nil {
		t.Fatalf("Set NX existing: %v", err)
	}
	if stored {
		t.Fatal("Set NX existing stored = true, want false")
	}

	entry, stored, err := store.Set("exec", "repo", []byte("two"), SetOptions{Mode: SetModeXX})
	if err != nil {
		t.Fatalf("Set XX existing: %v", err)
	}
	if !stored {
		t.Fatal("Set XX existing stored = false, want true")
	}
	if entry.Version != 2 {
		t.Fatalf("version = %d, want 2", entry.Version)
	}
}

func TestStoreSetRejectsInvalidMode(t *testing.T) {
	store := New(Options{})
	defer store.Close()

	_, _, err := store.Set("exec", "repo", []byte("one"), SetOptions{Mode: SetMode("BAD")})
	if !errors.Is(err, ErrInvalidSetMode) {
		t.Fatalf("Set invalid mode err = %v, want ErrInvalidSetMode", err)
	}
}

func TestStoreTTLAndExpire(t *testing.T) {
	store := New(Options{PruneInterval: 0})
	defer store.Close()
	now := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	_, stored, err := store.Set("exec", "repo", []byte("one"), SetOptions{TTL: time.Second})
	if err != nil {
		t.Fatalf("Set with TTL: %v", err)
	}
	if !stored {
		t.Fatal("Set with TTL stored = false, want true")
	}

	ttl, found, expiring, err := store.PTTL("exec", "repo")
	if err != nil {
		t.Fatalf("PTTL: %v", err)
	}
	if !found || !expiring || ttl != time.Second {
		t.Fatalf("PTTL ttl=%s found=%v expiring=%v, want 1s true true", ttl, found, expiring)
	}

	now = now.Add(1500 * time.Millisecond)
	_, found, err = store.Get("exec", "repo")
	if err != nil {
		t.Fatalf("Get expired: %v", err)
	}
	if found {
		t.Fatal("Get expired found = true, want false")
	}

	_, stored, err = store.Set("exec", "repo", []byte("two"), SetOptions{})
	if err != nil {
		t.Fatalf("Set without TTL: %v", err)
	}
	if !stored {
		t.Fatal("Set without TTL stored = false, want true")
	}
	ttl, found, expiring, err = store.PTTL("exec", "repo")
	if err != nil {
		t.Fatalf("PTTL no expiry: %v", err)
	}
	if !found || expiring || ttl != 0 {
		t.Fatalf("PTTL no expiry ttl=%s found=%v expiring=%v, want 0 true false", ttl, found, expiring)
	}

	expired, err := store.Expire("exec", "repo", time.Millisecond)
	if err != nil {
		t.Fatalf("Expire: %v", err)
	}
	if !expired {
		t.Fatal("Expire = false, want true")
	}
}

func TestStoreGetDelete(t *testing.T) {
	store := New(Options{})
	defer store.Close()
	_, _, err := store.Set("exec", "repo", []byte("value"), SetOptions{})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	entry, found, err := store.GetDelete("exec", "repo")
	if err != nil {
		t.Fatalf("GetDelete: %v", err)
	}
	if !found || string(entry.Value) != "value" {
		t.Fatalf("GetDelete found=%v value=%q, want value", found, string(entry.Value))
	}
	_, found, err = store.Get("exec", "repo")
	if err != nil {
		t.Fatalf("Get after GetDelete: %v", err)
	}
	if found {
		t.Fatal("Get after GetDelete found = true, want false")
	}
}

func TestStoreListAndBounds(t *testing.T) {
	store := New(Options{MaxEntries: 2, MaxValueBytes: 4})
	defer store.Close()

	for _, key := range []string{"a", "b", "c"} {
		_, _, err := store.Set("exec", key, []byte("ok"), SetOptions{})
		if err != nil {
			t.Fatalf("Set %s: %v", key, err)
		}
	}
	entries, err := store.List("exec", "", 0, false)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want max 2", len(entries))
	}
	if entries[0].Value != nil {
		t.Fatalf("entry value = %q, want omitted", string(entries[0].Value))
	}

	_, _, err = store.Set("exec", "too-large", []byte("12345"), SetOptions{})
	if !errors.Is(err, ErrValueTooLarge) {
		t.Fatalf("Set oversized err = %v, want ErrValueTooLarge", err)
	}
}

func TestStoreConfigureUpdatesPruneInterval(t *testing.T) {
	store := New(Options{PruneInterval: 0})
	defer store.Close()

	select {
	case <-store.pruneDone:
	default:
		t.Fatal("pruneDone should start closed when prune interval is disabled")
	}

	store.Configure(Options{PruneInterval: time.Millisecond})
	select {
	case <-store.pruneDone:
		t.Fatal("pruneDone should stay open after enabling prune interval")
	default:
	}

	store.Configure(Options{PruneInterval: 0})
	select {
	case <-store.pruneDone:
	default:
		t.Fatal("pruneDone should close after disabling prune interval")
	}
}
