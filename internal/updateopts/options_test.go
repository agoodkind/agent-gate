package updateopts

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/version"
	gkversion "goodkind.io/gklog/version"
)

func TestOptionsPreserveAgentGatePathsAndRollingDefault(t *testing.T) {
	stateHome := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	options := Options(&config.Config{}, Overrides{})

	if options.Config.Repo != config.DefaultUpdateRepo {
		t.Fatalf("Repo = %q, want %q", options.Config.Repo, config.DefaultUpdateRepo)
	}
	if options.Config.Binary != "agent-gate" {
		t.Fatalf("Binary = %q, want agent-gate", options.Config.Binary)
	}
	if options.Config.CurrentVersion != gkversion.Version {
		t.Fatalf("CurrentVersion = %q, want %q", options.Config.CurrentVersion, gkversion.Version)
	}
	if options.Config.CurrentCommit != gkversion.Commit {
		t.Fatalf("CurrentCommit = %q, want %q", options.Config.CurrentCommit, gkversion.Commit)
	}
	if options.Config.CurrentBuildHash != version.BuildHash() {
		t.Fatalf("CurrentBuildHash = %q, want %q", options.Config.CurrentBuildHash, version.BuildHash())
	}
	if options.Config.AllowPrerelease == nil {
		t.Fatal("AllowPrerelease = nil, want pointer")
	}
	if !*options.Config.AllowPrerelease {
		t.Fatal("AllowPrerelease = false, want true")
	}
	if options.Config.Interval != 24*time.Hour {
		t.Fatalf("Interval = %s, want 24h", options.Config.Interval)
	}
	if options.Config.APIBaseURLEnv != "AGENT_GATE_UPDATE_API_BASE_URL" {
		t.Fatalf("APIBaseURLEnv = %q, want AGENT_GATE_UPDATE_API_BASE_URL", options.Config.APIBaseURLEnv)
	}
	if options.StatePath != filepath.Join(stateHome, "agent-gate", "update.json") {
		t.Fatalf("StatePath = %q", options.StatePath)
	}
	if options.CacheDir != filepath.Join(cacheHome, "agent-gate", "update") {
		t.Fatalf("CacheDir = %q", options.CacheDir)
	}
}

func TestOptionsPreserveExplicitStableChannelAndOverrides(t *testing.T) {
	stable := false
	client := &http.Client{}
	logger := slog.Default()
	configValue := &config.Config{
		Update: config.Update{
			Repo:            "example/custom",
			Interval:        "48h",
			AllowPrerelease: &stable,
		},
	}

	options := Options(configValue, Overrides{
		Client:      client,
		InstallPath: "/tmp/agent-gate",
		DryRun:      true,
		Log:         logger,
	})

	if options.Config.Repo != "example/custom" {
		t.Fatalf("Repo = %q, want example/custom", options.Config.Repo)
	}
	if options.Config.AllowPrerelease == nil {
		t.Fatal("AllowPrerelease = nil, want pointer")
	}
	if *options.Config.AllowPrerelease {
		t.Fatal("AllowPrerelease = true, want false")
	}
	if options.Config.Interval != 48*time.Hour {
		t.Fatalf("Interval = %s, want 48h", options.Config.Interval)
	}
	if options.Client != client {
		t.Fatal("Client override was not preserved")
	}
	if options.InstallPath != "/tmp/agent-gate" {
		t.Fatalf("InstallPath = %q, want /tmp/agent-gate", options.InstallPath)
	}
	if !options.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if options.Log != logger {
		t.Fatal("Log override was not preserved")
	}
}
