package config

import (
	_ "embed"
	"fmt"
	"log/slog"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

//go:embed common_write_specs.toml
var commonWriteSpecSource []byte

var loadCommonWriteSpecs = sync.OnceValues(func() ([]ShellWriteSpec, error) {
	var document struct {
		WriteSpecs []ShellWriteSpec `toml:"write_specs"`
	}
	if err := toml.Unmarshal(commonWriteSpecSource, &document); err != nil {
		slog.Error("invalid common command descriptions", "err", err)
		return nil, fmt.Errorf("decode common command descriptions: %w", err)
	}
	for index, spec := range document.WriteSpecs {
		arguments := spec.ArgumentDescription()
		if err := ValidateArgumentSpec(&arguments); err != nil {
			slog.Error("invalid common command arguments", "description", index, "err", err)
			return nil, fmt.Errorf("common command description %d: %w", index, err)
		}
	}
	return document.WriteSpecs, nil
})

// CommonWriteSpecs returns explicit shipped descriptions for common file editors.
// A matching condition descriptor replaces the shipped argument interpretation.
func CommonWriteSpecs() ([]ShellWriteSpec, error) {
	return loadCommonWriteSpecs()
}
