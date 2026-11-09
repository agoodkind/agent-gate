package auditstorage_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/auditstorage"
)

func TestRecoveryFreezesPublishedScopeIncludingInterruptedPublication(t *testing.T) {
	catalog, policy, now := sizeCatalog(t)
	first, err := catalog.EnsureCurrent(t.Context(), policy, now)
	if err != nil {
		t.Fatal(err)
	}

	unrecorded := strings.TrimSuffix(first.Path, ".db") + "-00000000000000000041.db"
	database, err := auditstorage.OpenWriter(t.Context(), unrecorded)
	if err != nil {
		t.Fatal(err)
	}

	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	pass, err := catalog.Recovery(t.Context(), policy, now.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = pass.Close() }()
	later, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	backward, err := catalog.EnsureCurrent(t.Context(), policy, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]bool)

	for {
		files, err := pass.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		if len(files) > 100 {
			t.Fatalf("directory page contains %d files", len(files))
		}

		for _, file := range files {
			seen[file.Path] = true
		}
	}

	if !seen[first.Path] || !seen[unrecorded] || seen[later.Path] || seen[backward.Path] ||
		len(seen) != 2 {
		t.Fatalf("captured recovery scope = %v", seen)
	}
}
