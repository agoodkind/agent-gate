package installer

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
)

// CanonicalExecutablePath returns an absolute path with all symlinks resolved.
func CanonicalExecutablePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("--bin-path is required")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve absolute executable path %q: %w", path, err)
		slog.Warn("install executable absolute path resolution failed", "path", path, "err", wrappedErr)
		return "", wrappedErr
	}
	canonicalPath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil {
		wrappedErr := fmt.Errorf("resolve executable symlinks for %q: %w", absolutePath, err)
		slog.Warn("install executable symlink resolution failed", "path", absolutePath, "err", wrappedErr)
		return "", wrappedErr
	}
	return canonicalPath, nil
}
