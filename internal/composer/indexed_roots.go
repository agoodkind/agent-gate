package composer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultIndexedRootsTTL = 10 * time.Second
	indexedStatusToken     = "indexed"
)

// NewCachedIndexedRootsSource returns an lm-semantic-search root source with a
// short in-memory cache for hot hook paths.
func NewCachedIndexedRootsSource(binary string, ttl time.Duration) IndexedRootsSource {
	return newCachedIndexedRootsSource(binary, ttl, time.Now)
}

func newCachedIndexedRootsSource(binary string, ttl time.Duration, now func() time.Time) IndexedRootsSource {
	if ttl <= 0 {
		ttl = defaultIndexedRootsTTL
	}
	if now == nil {
		now = time.Now
	}
	source := &cachedIndexedRootsSource{
		binary:      binary,
		ttl:         ttl,
		now:         now,
		mu:          sync.Mutex{},
		cachedRoots: nil,
		expires:     time.Time{},
	}
	return source.roots
}

// DefaultIndexedRootsSource returns the local lm-semantic-search CLI source.
func DefaultIndexedRootsSource() IndexedRootsSource {
	return NewCachedIndexedRootsSource(defaultLMSBinary(), defaultIndexedRootsTTL)
}

type cachedIndexedRootsSource struct {
	binary      string
	ttl         time.Duration
	now         func() time.Time
	mu          sync.Mutex
	cachedRoots []string
	expires     time.Time
}

func (s *cachedIndexedRootsSource) roots(ctx context.Context) ([]string, error) {
	now := s.now()
	s.mu.Lock()
	if now.Before(s.expires) {
		roots := append([]string(nil), s.cachedRoots...)
		s.mu.Unlock()
		return roots, nil
	}
	s.mu.Unlock()

	roots, err := loadIndexedRoots(ctx, s.binary)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cachedRoots = append([]string(nil), roots...)
	s.expires = now.Add(s.ttl)
	s.mu.Unlock()
	return roots, nil
}

type lmsCodebaseList struct {
	Indexes []lmsCodebaseIndex `json:"indexes"`
}

type lmsCodebaseIndex struct {
	CanonicalPath string `json:"canonicalPath"`
	Status        string `json:"status"`
}

func loadIndexedRoots(ctx context.Context, binary string) ([]string, error) {
	if binary == "" {
		return nil, errors.New("lm-semantic-search binary unavailable")
	}
	slog.DebugContext(ctx, "loading indexed roots", "binary", binary)
	command := exec.CommandContext(ctx, binary, "--json", "codebase", "list")
	output, err := command.Output()
	if err != nil {
		slog.WarnContext(ctx, "load indexed roots command failed", "binary", binary, "err", err)
		return nil, fmt.Errorf("load indexed roots: %w", err)
	}
	var decoded lmsCodebaseList
	if err := json.Unmarshal(output, &decoded); err != nil {
		slog.WarnContext(ctx, "decode indexed roots failed", "binary", binary, "err", err)
		return nil, fmt.Errorf("decode indexed roots: %w", err)
	}
	roots := make([]string, 0, len(decoded.Indexes))
	for _, index := range decoded.Indexes {
		if !usableIndexedRoot(index) {
			continue
		}
		roots = append(roots, filepath.Clean(index.CanonicalPath))
	}
	slog.DebugContext(ctx, "loaded indexed roots", "count", len(roots))
	return roots, nil
}

func usableIndexedRoot(index lmsCodebaseIndex) bool {
	if index.CanonicalPath == "" || strings.HasPrefix(index.CanonicalPath, "chat://") {
		return false
	}
	return strings.Contains(strings.ToLower(index.Status), indexedStatusToken)
}

func defaultLMSBinary() string {
	if override := strings.TrimSpace(os.Getenv("AGENT_GATE_LMS_BIN")); override != "" {
		return override
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "bin", "lm-semantic-search")
}
