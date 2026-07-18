package config_test

import (
	"strings"
	"testing"

	"goodkind.io/agent-gate/internal/config"
)

// TestBlockFooterSubstitutesIntakeID confirms the block footer substitutes both the
// {intake_id} and {event_id} tokens with the intake event id, that an unset footer
// returns empty, and that a nil config is safe.
func TestBlockFooterSubstitutesIntakeID(t *testing.T) {
	var cfg config.Config
	cfg.Messages.BlockFooter = "report intake_id={intake_id} event={event_id}"

	got := cfg.BlockFooter("intake_abc123")
	if !strings.Contains(got, "intake_id=intake_abc123") {
		t.Fatalf("footer did not substitute {intake_id}: %q", got)
	}
	if !strings.Contains(got, "event=intake_abc123") {
		t.Fatalf("footer did not substitute {event_id}: %q", got)
	}

	var empty config.Config
	if empty.BlockFooter("intake_abc123") != "" {
		t.Fatalf("unset footer should return empty, got %q", empty.BlockFooter("intake_abc123"))
	}

	var nilCfg *config.Config
	if nilCfg.BlockFooter("intake_abc123") != "" {
		t.Fatal("nil config footer should return empty")
	}
}
