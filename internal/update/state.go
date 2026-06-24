package update

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"goodkind.io/agent-gate/internal/config"
)

// State stores the latest updater observation and result.
type State struct {
	LastCheckAt        time.Time `json:"last_check_at"`
	NextCheckAt        time.Time `json:"next_check_at"`
	LatestTag          string    `json:"latest_tag,omitempty"`
	AppliedTag         string    `json:"applied_tag,omitempty"`
	InstalledVersion   string    `json:"installed_version,omitempty"`
	InstalledCommit    string    `json:"installed_commit,omitempty"`
	InstalledBuildHash string    `json:"installed_build_hash,omitempty"`
	LastResult         string    `json:"last_result,omitempty"`
	LastError          string    `json:"last_error,omitempty"`
}

// LoadState reads the persisted updater state file.
func LoadState(path string) (State, error) {
	log := slog.Default()
	statePath := resolveStatePath(path)
	content, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return State{
				LastCheckAt:        time.Time{},
				NextCheckAt:        time.Time{},
				LatestTag:          "",
				AppliedTag:         "",
				InstalledVersion:   "",
				InstalledCommit:    "",
				InstalledBuildHash: "",
				LastResult:         "",
				LastError:          "",
			}, nil
		}
		log.Warn("update state read failed", "path", statePath, "err", err)
		return State{}, fmt.Errorf("read update state: %w", err)
	}
	var state State
	if err := json.Unmarshal(content, &state); err != nil {
		log.Warn("update state decode failed", "path", statePath, "err", err)
		return State{}, fmt.Errorf("decode update state: %w", err)
	}
	return state, nil
}

// SaveState writes the persisted updater state file atomically.
func SaveState(path string, state State) error {
	log := slog.Default()
	statePath := resolveStatePath(path)
	log.Info("update state save", "path", statePath)
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		log.Warn("update state create dir failed", "path", statePath, "err", err)
		return fmt.Errorf("create update state dir: %w", err)
	}
	content, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		log.Warn("update state encode failed", "path", statePath, "err", err)
		return fmt.Errorf("encode update state: %w", err)
	}
	content = append(content, '\n')
	tmpPath := statePath + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o600); err != nil {
		log.Warn("update state temp write failed", "path", tmpPath, "err", err)
		return fmt.Errorf("write update state temp: %w", err)
	}
	if err := os.Rename(tmpPath, statePath); err != nil {
		log.Warn("update state replace failed", "path", statePath, "err", err)
		return fmt.Errorf("replace update state: %w", err)
	}
	return nil
}

func resolveStatePath(path string) string {
	if path != "" {
		return path
	}
	return config.DefaultUpdateStatePath()
}
