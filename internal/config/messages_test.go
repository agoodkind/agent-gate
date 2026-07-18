package config_test

import (
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// TestBlockFooter confirms BlockFooter returns the configured message trimmed, and
// empty for an unset footer or a nil config.
func TestBlockFooter(t *testing.T) {
	var cfg config.Config
	cfg.Messages.BlockFooter = "  Report the agent-gate intake id to the user.  "
	if got := cfg.BlockFooter(); got != "Report the agent-gate intake id to the user." {
		t.Fatalf("BlockFooter = %q, want the trimmed configured message", got)
	}

	var empty config.Config
	if empty.BlockFooter() != "" {
		t.Fatalf("unset footer should return empty, got %q", empty.BlockFooter())
	}

	var nilCfg *config.Config
	if nilCfg.BlockFooter() != "" {
		t.Fatal("nil config footer should return empty")
	}
}
