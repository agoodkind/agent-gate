package canonpath

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolveFollowsSymlinkToCanonicalReal(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	viaLink := Resolve("", link)
	viaReal := Resolve("", real)

	if !viaLink.IsCanonical {
		t.Fatalf("expected symlinked path to resolve canonically")
	}
	if viaLink.Canonical != viaReal.Canonical {
		t.Fatalf("symlink and real path should share a canonical path: %q vs %q", viaLink.Canonical, viaReal.Canonical)
	}
	if viaLink.Raw == viaLink.Canonical {
		t.Fatalf("expected raw (%q) to differ from canonical (%q) through a symlink", viaLink.Raw, viaLink.Canonical)
	}
}

func TestResolveNonexistentFallsBackWithoutError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	got := Resolve("", missing)

	if got.IsCanonical {
		t.Fatalf("expected non-existent path to be non-canonical")
	}
	if got.Canonical != got.Raw {
		t.Fatalf("fallback canonical (%q) should equal raw (%q)", got.Canonical, got.Raw)
	}
}

func TestResolveJoinsRelativeAgainstCwd(t *testing.T) {
	cwd := t.TempDir()

	got := Resolve(cwd, "child")

	want := filepath.Join(cwd, "child")
	if got.Raw != want {
		t.Fatalf("expected relative path joined to cwd %q, got %q", want, got.Raw)
	}
}

func TestCacheResolveMatchesPureResolve(t *testing.T) {
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	cache := NewCache(time.Minute)

	first := cache.Resolve("", link)
	second := cache.Resolve("", link)
	pure := Resolve("", link)

	if first != second {
		t.Fatalf("cache returned inconsistent results: %+v vs %+v", first, second)
	}
	if first.Canonical != pure.Canonical {
		t.Fatalf("cache canonical %q should match pure resolve %q", first.Canonical, pure.Canonical)
	}
}
