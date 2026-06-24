package update

import (
	"path/filepath"
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
