package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
	"goodkind.io/agent-gate/internal/hotkv"
)

func TestCacheAggregateLimitDefaultsOverridesAndValidation(t *testing.T) {
	for _, value := range []int{0, 1, 4096, hotkv.DefaultMaxBytes} {
		path := filepath.Join(t.TempDir(), "config.toml")
		body := fmt.Sprintf("[performance.hook.cache]\nmax_bytes = %d\n", value)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}

		cfg, err := config.LoadExisting(path)
		if err != nil {
			t.Fatal(err)
		}

		expected := value
		if expected == 0 {
			expected = hotkv.DefaultMaxBytes
		}

		if cfg.HookCacheMaxBytes() != expected {
			t.Fatalf("limit = %d, want %d", cfg.HookCacheMaxBytes(), expected)
		}
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(
		path,
		[]byte("[performance.hook.cache]\nmax_bytes = -1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	for _, load := range []func(string) (*config.Config, error){
		config.LoadExisting,
		config.LoadDegradedPath,
	} {
		if _, err := load(path); err == nil ||
			!strings.Contains(err.Error(), "performance.hook.cache.max_bytes") {
			t.Fatalf("invalid aggregate limit did not fail explicitly: %v", err)
		}
	}
}
