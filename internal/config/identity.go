package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"

	"github.com/BurntSushi/toml"
)

// Identity returns the exact source-byte hash or a deterministic structural
// hash for a configuration created in memory.
func (c *Config) Identity() (string, error) {
	if c == nil {
		return "", errors.New("config identity requires a non-nil config")
	}
	if c.sourceIdentity != "" {
		return c.sourceIdentity, nil
	}
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(c); err != nil {
		slog.Warn("encode structural config identity failed", "err", err)
		return "", fmt.Errorf("encode structural config identity: %w", err)
	}
	return hashIdentity(encoded.Bytes()), nil
}

func hashIdentity(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}
