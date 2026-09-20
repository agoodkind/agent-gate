package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// PrepareReplacement parses a complete candidate and retains the active path.
func PrepareReplacement(candidatePath string) (*DefaultsPlan, error) {
	configPath := filepath.Clean(Path())
	initialPathState, err := captureDefaultsPathState(configPath)
	if err != nil {
		return nil, err
	}
	candidateBytes, err := os.ReadFile(candidatePath)
	if err != nil {
		slog.Warn(
			"read recovery candidate failed",
			slog.String("path", candidatePath),
			slog.Any("err", err),
		)
		return nil, fmt.Errorf("read recovery candidate: %w", err)
	}
	preparedConfig, err := loadSource(configPath, candidateBytes, true)
	if err != nil {
		return nil, err
	}
	pathState, err := captureDefaultsPathState(configPath)
	if err != nil {
		return nil, err
	}
	if !sameDefaultsPathState(initialPathState, pathState) {
		return nil, reportDefaultsPreparationError(
			"revalidate config path",
			errors.New("config path identity changed during preparation"),
		)
	}
	if err := retainDefaultsPathHandles(&pathState); err != nil {
		return nil, err
	}
	plan := &DefaultsPlan{
		Path:         configPath,
		Content:      append([]byte(nil), candidateBytes...),
		Config:       preparedConfig,
		applyPath:    pathState.applyPath,
		content:      append([]byte(nil), candidateBytes...),
		path:         configPath,
		pathState:    pathState,
		beforeRename: nil,
		applyMutex:   sync.Mutex{},
		consumed:     false,
	}
	runtime.SetFinalizer(plan, closeDefaultsPlanHandles)
	return plan, nil
}
