package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ClaudeAdapter struct{}

func (ClaudeAdapter) Name() string { return "claude" }

// FindRealBinary locates the actual claude binary, skipping any agent-gate
// wrapper that may be installed earlier in PATH.
func (ClaudeAdapter) FindRealBinary() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to determine own path: %w", err)
	}
	self, err = filepath.EvalSymlinks(self)
	if err != nil {
		return "", fmt.Errorf("failed to resolve own path: %w", err)
	}
	selfDir := filepath.Dir(self)

	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, "claude")
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil {
			continue
		}
		if resolved == self || filepath.Dir(resolved) == selfDir {
			continue
		}
		if strings.ToLower(filepath.Base(resolved)) == "agent-gate" {
			continue
		}
		return candidate, nil
	}

	return "", fmt.Errorf("real claude binary not found in PATH (is it installed?)")
}
