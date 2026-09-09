package version

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBuildIdentityReadsOnceAndSurvivesReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-gate")
	original := []byte("first executable")
	if err := os.WriteFile(path, original, 0o700); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	identity := newBuildIdentity(
		func() (string, error) { return path, nil },
		func(name string) (io.ReadCloser, error) {
			reads.Add(1)
			return os.Open(name)
		},
	)
	if err := identity.initialize(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(original)
	want := hex.EncodeToString(digest[:])[:12]
	if err := os.WriteFile(path, []byte("replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			if got := identity.value(); got != want {
				t.Errorf("hash = %q, want %q", got, want)
			}
		})
	}
	workers.Wait()
	if got := reads.Load(); got != 1 {
		t.Fatalf("executable reads = %d, want 1", got)
	}
}

func TestBuildIdentityCachesReadFailureWithoutPublishingPartialHash(t *testing.T) {
	wantErr := errors.New("read failed")
	var reads atomic.Int64
	var closes atomic.Int64
	identity := newBuildIdentity(
		func() (string, error) { return "agent-gate", nil },
		func(string) (io.ReadCloser, error) {
			reads.Add(1)
			return &errorAfterReader{
				reader: strings.NewReader("partial executable"),
				err:    wantErr,
				closes: &closes,
			}, nil
		},
	)

	if err := identity.initialize(); !errors.Is(err, wantErr) {
		t.Fatalf("initialize error = %v, want %v", err, wantErr)
	}
	if got := identity.value(); got != "unknown" {
		t.Fatalf("hash = %q, want unknown", got)
	}
	if err := identity.initialize(); !errors.Is(err, wantErr) {
		t.Fatalf("second initialize error = %v, want %v", err, wantErr)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("executable reads = %d, want 1", got)
	}
	if got := closes.Load(); got != 1 {
		t.Fatalf("executable closes = %d, want 1", got)
	}
}

type errorAfterReader struct {
	reader *strings.Reader
	err    error
	closes *atomic.Int64
}

func (reader *errorAfterReader) Read(buffer []byte) (int, error) {
	count, err := reader.reader.Read(buffer)
	if err == nil {
		return count, nil
	}
	if errors.Is(err, io.EOF) {
		return 0, reader.err
	}
	return 0, err
}

func (reader *errorAfterReader) Close() error {
	reader.closes.Add(1)
	return nil
}
