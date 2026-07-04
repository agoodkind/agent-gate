// Package updateopts adapts agent-gate configuration to selfupdate options.
package updateopts

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/version"
	gkversion "goodkind.io/gklog/version"
	"goodkind.io/go-makefile/selfupdate"
)

// Overrides carries operation-specific update settings.
type Overrides struct {
	Client      *http.Client
	InstallPath string
	DryRun      bool
	Log         *slog.Logger
}

// Options builds selfupdate options while preserving agent-gate's on-disk paths.
func Options(cfg *config.Config, overrides Overrides) selfupdate.Options {
	allowPrerelease := true
	if cfg != nil {
		allowPrerelease = cfg.UpdateAllowPrerelease()
	}
	return selfupdate.Options{
		Config: selfupdate.Config{
			Repo:             updateRepo(cfg),
			Binary:           "agent-gate",
			CurrentVersion:   gkversion.Version,
			CurrentCommit:    gkversion.Commit,
			CurrentBuildHash: version.BuildHash(),
			CurrentDirty:     gkversion.Dirty == "true",
			AllowPrerelease:  &allowPrerelease,
			Interval:         updateInterval(cfg),
			APIBaseURLEnv:    "AGENT_GATE_UPDATE_API_BASE_URL",
		},
		Client:      overrides.Client,
		InstallPath: overrides.InstallPath,
		CacheDir:    filepath.Join(config.DefaultCacheDir(), "update"),
		StatePath:   config.DefaultUpdateStatePath(),
		DryRun:      overrides.DryRun,
		Log:         overrides.Log,
	}
}

func updateRepo(cfg *config.Config) string {
	if cfg == nil {
		return config.DefaultUpdateRepo
	}
	return cfg.UpdateRepo()
}

func updateInterval(cfg *config.Config) time.Duration {
	if cfg == nil {
		return 24 * time.Hour
	}
	return cfg.UpdateInterval()
}
