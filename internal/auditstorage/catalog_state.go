package auditstorage

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
)

// CatalogState records the active policy or the families awaiting destruction.
type CatalogState struct {
	CleanupError     string   `json:"cleanup_error,omitempty"`
	BasePath         string   `json:"base_path"`
	IntervalSeconds  int64    `json:"interval_seconds"`
	Retained         int      `json:"retention_buckets"`
	ResetPending     bool     `json:"reset_pending"`
	PendingBasePaths []string `json:"pending_base_paths,omitempty"`
}

func (catalog *Catalog) loadState() (CatalogState, error) {
	var state CatalogState
	data, err := os.ReadFile(catalog.options.StatePath)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, storageError("read catalog state", err)
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, storageError("decode catalog state", err)
	}
	if state.BasePath == "" {
		return state, errors.New("catalog state has no base path")
	}
	return state, nil
}

func (catalog *Catalog) saveState(state CatalogState) error {
	slog.Debug("save audit catalog state", "reset_pending", state.ResetPending)
	if err := catalog.checkpoint("state", catalog.options.StatePath); err != nil {
		return err
	}
	directory := filepath.Dir(catalog.options.StatePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return storageError("create catalog state directory", err)
	}
	data, err := json.Marshal(state)
	if err != nil {
		return storageError("encode catalog state", err)
	}
	file, err := os.CreateTemp(directory, ".audit-storage-*")
	if err != nil {
		return storageError("create catalog state", err)
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return storageError("write catalog state", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return storageError("sync catalog state", err)
	}
	if err := file.Close(); err != nil {
		return storageError("close catalog state", err)
	}
	if err := os.Rename(file.Name(), catalog.options.StatePath); err != nil {
		return storageError("publish catalog state", err)
	}
	return syncDirectory(directory)
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return storageError("open directory for sync", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return storageError("sync directory "+path, err)
	}
	return nil
}

func pendingFamilies(state CatalogState, base string) []string {
	paths := append([]string{}, state.PendingBasePaths...)
	if state.BasePath != "" {
		paths = append(paths, state.BasePath)
	}
	paths = append(paths, base)
	slices.Sort(paths)
	return slices.Compact(paths)
}

// canonicalPath resolves existing ancestors without creating the audit family.
func canonicalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("catalog path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", storageError("canonicalize "+path, err)
	}
	ancestor := absolute
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			for _, component := range slices.Backward(missing) {
				resolved = filepath.Join(resolved, component)
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", storageError("canonicalize "+path, err)
		}
		missing = append(missing, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", storageError("canonicalize "+path, err)
		}
		ancestor = parent
	}
}
