package version

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
)

// Set via ldflags at build time.
var (
	Commit  = "unknown"
	Version = "dev"
	Dirty   = "false"
)

// BuildHash computes the SHA-256 of the running binary, truncated to 12 hex chars.
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

// Attrs returns slog attributes for build metadata.
func Attrs() []slog.Attr {
	return []slog.Attr{
		slog.String("commit", Commit),
		slog.String("version", Version),
		slog.String("buildHash", BuildHash()),
		slog.String("dirty", Dirty),
	}
}
