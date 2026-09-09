// Package version computes the runtime hash of the on-disk binary for audit
// stamping. Release identity (version, commit, dirty) lives in gklog/version,
// the shared stamp used across every daemon consumer.
package version

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"
)

type buildIdentity struct {
	once    sync.Once
	resolve func() (string, error)
	open    func(string) (io.ReadCloser, error)
	hash    string
	err     error
}

type executableOpener struct {
	open func(string) (*os.File, error)
}

func (opener executableOpener) reader(name string) (io.ReadCloser, error) {
	reader, err := opener.open(name)
	return reader, err
}

var processBuildIdentity = newBuildIdentity(
	os.Executable,
	executableOpener{open: os.Open}.reader,
)

func newBuildIdentity(
	resolve func() (string, error),
	open func(string) (io.ReadCloser, error),
) *buildIdentity {
	return &buildIdentity{
		once: sync.Once{}, resolve: resolve, open: open, hash: "", err: nil,
	}
}

func (identity *buildIdentity) initialize() error {
	identity.once.Do(func() {
		path, err := identity.resolve()
		if err != nil {
			identity.err = fmt.Errorf("resolve executable: %w", err)
			return
		}
		reader, err := identity.open(path)
		if err != nil {
			identity.err = fmt.Errorf("open executable: %w", err)
			return
		}
		digest := sha256.New()
		_, copyErr := io.Copy(digest, reader)
		closeErr := reader.Close()
		if copyErr != nil {
			identity.err = fmt.Errorf("hash executable: %w", copyErr)
			return
		}
		if closeErr != nil {
			identity.err = fmt.Errorf("close executable after hashing: %w", closeErr)
			return
		}
		identity.hash = hex.EncodeToString(digest.Sum(nil))[:12]
	})
	return identity.err
}

func (identity *buildIdentity) value() string {
	if err := identity.initialize(); err != nil {
		return "unknown"
	}
	return identity.hash
}

// Initialize computes and freezes the running process identity.
func Initialize() error {
	return processBuildIdentity.initialize()
}

// BuildHash returns the cached SHA-256 process identity, truncated to 12 hex
// characters, or "unknown" when initialization failed.
func BuildHash() string {
	return processBuildIdentity.value()
}
