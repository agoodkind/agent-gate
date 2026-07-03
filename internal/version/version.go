// Package version computes the runtime hash of the on-disk binary for audit
// stamping. Release identity (version, commit, dirty) lives in gklog/version,
// the shared stamp used across every daemon consumer.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// BuildHash computes the SHA-256 of the running binary, truncated to
// 12 hex characters.
func BuildHash() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	_, _ = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil))[:12]
}
