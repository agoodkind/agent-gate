package update

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSaveAndLoadState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	state := State{}
	state.LastCheckAt = time.Unix(100, 0).UTC()
	state.NextCheckAt = time.Unix(200, 0).UTC()
	state.LatestTag = "v1"
	state.AppliedTag = "v0"
	state.InstalledVersion = "v0"
	state.InstalledCommit = "abc123"
	state.InstalledBuildHash = "deadbeef"
	state.LastResult = "check"
	state.LastError = "none"

	if err := SaveState(path, state); err != nil {
		t.Fatalf("SaveState() error: %v", err)
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if got != state {
		t.Fatalf("round trip state = %#v, want %#v", got, state)
	}
}

func TestSaveStateConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	states := []State{
		{LatestTag: "v1", LastResult: "check"},
		{LatestTag: "v2", LastResult: "applied"},
	}
	var waitGroup sync.WaitGroup
	errs := make(chan error, len(states))
	for _, state := range states {
		waitGroup.Add(1)
		go func(state State) {
			defer waitGroup.Done()
			errs <- SaveState(path, state)
		}(state)
	}
	waitGroup.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("SaveState() error: %v", err)
		}
	}
	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState() error: %v", err)
	}
	if got.LatestTag != "v1" && got.LatestTag != "v2" {
		t.Fatalf("LatestTag = %q, want one of saved values", got.LatestTag)
	}
}
